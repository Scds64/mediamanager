package bot

// TG /update 命令：拉取最新镜像并重建自身容器。
// 原理：镜像内装 docker-cli + 镜像内置 compose v2（Dockerfile 安装）+ 挂载 /var/run/docker.sock；
// 重建动作放到独立的"目标镜像"容器里执行（--entrypoint docker-compose），旧容器（bot）被替换时更新进程不受影响。
// 环境变量（docker-compose.yml 注入）：
//   ENV_UPDATE_IMAGE       要拉取的镜像名，如 user/mediamanager:latest（须与 compose 的 image 字段一致）
//   ENV_COMPOSE_DIR        宿主部署目录（内含 docker-compose.yml），如 /mnt/mydisk/mediaman
//   ENV_UPDATE_CONTAINER   容器名（"已是最新版本"判断用），默认 Mediamanager，可选

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// docker/compose 官方镜像的 latest 标签是 Python 版 v1.26.2（2020 年），解析不了现代 compose 文件，
// 故不再使用外部 compose 镜像：Dockerfile 已把 compose v2 二进制（docker-compose）装进本镜像，
// /update 用刚拉取的目标镜像自身启动独立运行器容器执行重建，任何环境（挂载 docker.sock 即可）都一致可用。
const composeRunnerEntrypoint = "docker-compose"

// handleUpdate /update 检查并更新自身容器：拉取新镜像 → 分离容器执行 compose 重建（约 1-2 分钟，期间服务短暂中断）。
func (b *Bot) handleUpdate(msg *tgbotapi.Message) {
	if !b.isAdmin(msg) {
		return
	}
	// 防止连点导致并发重建
	if !b.updateMu.TryLock() {
		b.SubmitSend(func() { b.SendReply(msg, "⚠️ 已有更新任务在执行中，请稍后再试。") })
		return
	}
	go func() {
		defer b.updateMu.Unlock()
		b.runUpdate(msg)
	}()
}

func (b *Bot) runUpdate(msg *tgbotapi.Message) {
	image := os.Getenv("ENV_UPDATE_IMAGE")
	if image == "" {
		b.SubmitSend(func() {
			b.SendReply(msg, "❌ 未配置更新镜像，请在 docker-compose.yml 设置 ENV_UPDATE_IMAGE。")
		})
		return
	}
	composeDir := os.Getenv("ENV_COMPOSE_DIR")
	if composeDir == "" {
		b.SubmitSend(func() {
			b.SendReply(msg, "❌ 未配置宿主部署目录，请在 docker-compose.yml 设置 ENV_COMPOSE_DIR。")
		})
		return
	}
	// 容器名（"已是最新版本"判断用），默认 Mediamanager，可用 ENV_UPDATE_CONTAINER 覆盖
	container := os.Getenv("ENV_UPDATE_CONTAINER")
	if container == "" {
		container = "Mediamanager"
	}
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		b.SubmitSend(func() {
			b.SendReply(msg, "❌ 容器未挂载 /var/run/docker.sock，无法操作宿主 docker。请更新 docker-compose.yml 后重建容器。")
		})
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		b.SubmitSend(func() {
			b.SendReply(msg, "❌ 镜像内缺少 docker-cli（旧镜像），请重新构建镜像后再使用 /update。")
		})
		return
	}
	if _, err := exec.LookPath(composeRunnerEntrypoint); err != nil {
		b.SubmitSend(func() {
			b.SendReply(msg, "❌ 镜像内缺少 compose v2（旧镜像），请更新到新镜像后再使用 /update。")
		})
		return
	}

	b.SubmitSend(func() { b.SendReply(msg, fmt.Sprintf("🔍 开始检查更新（%s）...", image)) })

	// 对比拉取前后的镜像 ID，且与运行中容器的镜像 ID 一起判断：
	// 标签无变化但容器仍落后（上次拉新镜像后未重建）时也要重建。
	beforeID := dockerInspectID(image)
	if out, err := exec.Command("docker", "pull", image).CombinedOutput(); err != nil {
		b.SubmitSend(func() { b.SendReply(msg, fmt.Sprintf("❌ 拉取镜像失败：\n%s", truncateLog(string(out)))) })
		return
	}
	afterID := dockerInspectID(image)
	containerID := dockerContainerImageID(container)
	if afterID != "" && afterID == beforeID && afterID == containerID {
		b.SubmitSend(func() { b.SendReply(msg, "✅ 已是最新版本，无需更新。") })
		log.Printf("[Bot] /update 检查完成：镜像无更新（%s）", image)
		return
	}

	b.SubmitSend(func() {
		b.SendReply(msg, "🔄 新镜像已拉取，即将重建容器（约 1-2 分钟，期间服务短暂中断，完成后会自动恢复）...")
	})
	log.Printf("[Bot] /update 镜像已更新（%s），准备重建容器", image)

	// 等上面的消息发出后再重建（重建会替换当前容器进程）
	time.Sleep(4 * time.Second)

	// 分离启动更新容器：用刚拉取的目标镜像（内置 compose v2）作为运行器，
	// 挂载宿主 socket 与部署目录，用宿主 compose 文件重建自身。
	// 注意：不带 --pull never —— compose v1/v2 的 up 都要求镜像已存在即可，无需控制拉取策略。
	args := []string{
		"run", "-d", "--rm",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", composeDir + ":" + composeDir,
		"-w", composeDir,
		"--entrypoint", composeRunnerEntrypoint,
		image,
		"up", "-d", "--force-recreate",
	}
	cmd := exec.Command("docker", args...)
	if err := cmd.Start(); err != nil {
		b.SubmitSend(func() { b.SendReply(msg, fmt.Sprintf("❌ 启动重建任务失败：%v", err)) })
		return
	}
	log.Printf("[Bot] /update 重建任务已分离启动: docker %s", strings.Join(args, " "))
}

// dockerInspectID 返回镜像 tag 当前指向的镜像 ID（不存在返回空）。
func dockerInspectID(image string) string {
	out, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return ""
	}
	return normalizeImageID(strings.TrimSpace(string(out)))
}

// dockerContainerImageID 返回运行中容器使用的镜像 ID（取不到返回空）。
func dockerContainerImageID(container string) string {
	out, err := exec.Command("docker", "inspect", "--format", "{{.Image}}", container).Output()
	if err != nil {
		return ""
	}
	return normalizeImageID(strings.TrimSpace(string(out)))
}

// normalizeImageID 统一镜像 ID 格式（去掉 sha256: 前缀），便于比较。
func normalizeImageID(s string) string {
	return strings.TrimPrefix(s, "sha256:")
}

// truncateLog 截断过长的命令输出，避免 TG 消息超限。
func truncateLog(s string) string {
	const max = 900
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...(已截断)"
}
