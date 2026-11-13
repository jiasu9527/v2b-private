#!/bin/bash
# 完整更新脚本（包含前端编译）

set -e
cd /etc/forest

echo "=== 1. 更新代码 ==="
git pull origin master

echo ""
echo "=== 2. 更新数据库 ==="
./update.sh

echo ""
echo "=== 3. 编译前端 ==="
if ! command -v npm &> /dev/null; then
    echo "npm未安装，跳过前端编译"
else
    cd admin-src
    if [ ! -d "node_modules" ]; then
        npm install
    fi
    rm -rf dist ../public/assets/admin/*
    npm run build
    cd ..
fi

echo ""
echo "=== 4. 重启服务 ==="
./scripts/appctl restart

echo ""
echo "=== 完成！==="
echo "现在浏览器按 Ctrl+Shift+R 刷新页面"
