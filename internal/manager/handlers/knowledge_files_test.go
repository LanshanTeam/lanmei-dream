package handlers

import "testing"

// TestImportFileName 覆盖导入文件名的校验与清洗。
func TestImportFileName(t *testing.T) {
	tests := []struct {
		raw     string
		want    string // err 为 nil 时校验返回的文件名
		wantErr bool
	}{
		// 正常文件名
		{raw: "rules.csv", want: "rules.csv"},
		{raw: "知识库.md", want: "知识库.md"},
		{raw: "RULES.CSV", want: "RULES.CSV"},
		{raw: "   spaced.md   ", want: "spaced.md"},
		// 带路径的上传名：只取最后一段（浏览器可能上传完整路径）
		{raw: `C:\tmp\dir\a.md`, want: "a.md"},
		{raw: "/tmp/dir/b.csv", want: "b.csv"},
		// 逃逸输入：末段合法时按普通文件名处理，落点仍在导入目录内
		{raw: "../../etc/passwd.md", want: "passwd.md"},
		// 拒绝：空名 / 非白名单扩展名 / 隐藏文件（含写入用的 .part 临时文件）
		{raw: "", wantErr: true},
		{raw: ".", wantErr: true},
		{raw: "..", wantErr: true},
		{raw: "../../etc/passwd", wantErr: true},
		{raw: ".hidden.md", wantErr: true},
		{raw: "rules.txt", wantErr: true},
		{raw: "rules", wantErr: true},
		{raw: "rule.md.part", wantErr: true},
	}
	for _, tt := range tests {
		got, err := importFileName(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("importFileName(%q) 期望报错，实际返回 %q", tt.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("importFileName(%q) 期望通过，实际报错 %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("importFileName(%q) = %q，期望 %q", tt.raw, got, tt.want)
		}
	}
}
