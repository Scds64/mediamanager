package strm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 编目 orphan 记录的 target_path 存的是本地 STRM 路径，深度删除必须删掉它。
// 回归：该路径曾被当成网盘路径反推，得到 /media/media/... 后静默跳过。
func TestDeleteItemsOrphanTargetPath(t *testing.T) {
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "电影", "马鲁姆 (2023)")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	strmPath := filepath.Join(mediaDir, "马鲁姆.Malum.2023.strm")
	if err := os.WriteFile(strmPath, []byte("http://x/api/strm?id=1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 模拟 web 深度删除：只给 PanPath（= 整理历史的 target_path），Path 留空
	items := []DeleteItem{{Name: "马鲁姆", PanPath: filepath.ToSlash(strmPath)}}
	result := DeleteItems(context.Background(), nil, items, nil, nil, "/media#影视", nil)

	if _, err := os.Stat(strmPath); !os.IsNotExist(err) {
		t.Fatalf("本地 STRM 未被删除: %v", err)
	}
	if result.Success != 1 || len(result.FailList) != 0 {
		t.Fatalf("success=%d fail=%v", result.Success, result.FailList)
	}
	if _, err := os.Stat(mediaDir); !os.IsNotExist(err) {
		t.Fatalf("空目录未清理: %v", err)
	}
}
