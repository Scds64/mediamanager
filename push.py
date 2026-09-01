# -*- coding: utf-8 -*-
"""推送二进制和模板到远程服务器并重启容器。"""
import paramiko
import os

HOST = "172.20.10.2"
PORT = 22
USER = "root"
PASS = "password"
LOCAL_BIN = "mmbot_linux"
REMOTE_BIN = "/mnt/mydisk/mediaman/mmbot"
LOCAL_TPL_DIR = "templates"
REMOTE_TPL_DIR = "/mnt/mydisk/mediaman/templates"

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(HOST, PORT, USER, PASS)
sftp = ssh.open_sftp()

# 上传二进制
sftp.put(LOCAL_BIN, REMOTE_BIN)
print("二进制已上传到", REMOTE_BIN)
ssh.exec_command("chmod +x " + REMOTE_BIN)

# 递归上传模板目录
def upload_dir(local, remote):
    for root, dirs, files in os.walk(local):
        rel = os.path.relpath(root, local)
        if rel == ".":
            target = remote
        else:
            target = remote + "/" + rel.replace("\\", "/")
        try:
            sftp.stat(target)
        except FileNotFoundError:
            sftp.mkdir(target)
        for f in files:
            local_f = os.path.join(root, f)
            remote_f = target + "/" + f
            sftp.put(local_f, remote_f)
            print(f"模板已上传: {remote_f}")

upload_dir(LOCAL_TPL_DIR, REMOTE_TPL_DIR)
print("模板已上传到", REMOTE_TPL_DIR)

# 确保 docker-compose 有模板 bind mount
_, stdout, stderr = ssh.exec_command(
    "grep -q '/app/templates' /mnt/mydisk/mediaman/docker-compose.yml && "
    'echo "exists" || echo "missing"'
)
check = stdout.read().decode().strip()
if check == "missing":
    print("docker-compose 缺少模板 bind mount，正在添加...")
    _, stdout, stderr = ssh.exec_command(
        "sed -i '/- \\/mnt\\/mydisk\\/strm:\\/media/a\\      - /mnt/mydisk/mediaman/templates:/app/templates' "
        "/mnt/mydisk/mediaman/docker-compose.yml"
    )
    err = stderr.read().decode()
    if err:
        print("添加 bind mount 失败:", err)
    else:
        print("已添加 templates bind mount 到 docker-compose.yml")

sftp.close()

# 重建镜像并重启容器（docker restart 不会应用 compose 改动，如 mem_limit/GOMEMLIMIT）
_, stdout, stderr = ssh.exec_command("cd /mnt/mydisk/mediaman && docker-compose up -d --build 2>&1")
print(stdout.read().decode())
err = stderr.read().decode()
if err:
    print("stderr:", err)
ssh.close()
print("推送完成")