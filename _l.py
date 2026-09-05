import paramiko

c = paramiko.SSHClient()
c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect('172.20.10.2', 22, 'root', 'password')

# 检查文件是否存在，用更健壮的方式
stdin, stdout, stderr = c.exec_command("""docker exec Mediamanager sh -c '
cd "/media/电视剧/欧美剧/霹雳游侠 (1982) {tmdbid=2384}/Season 1"
ls -la "霹雳游侠.1982.S01E01.2160p.BluRay REMUX HDR10.H265.HDR10.DTS-HD MA 2.0.strm" 2>&1
echo "---"
head -c 500 "霹雳游侠.1982.S01E01.2160p.BluRay REMUX HDR10.H265.HDR10.DTS-HD MA 2.0.strm" 2>&1
'""")
print("结果:", stdout.read().decode('utf-8', errors='replace'))
print("ERR:", stderr.read().decode('utf-8', errors='replace'))

c.close()