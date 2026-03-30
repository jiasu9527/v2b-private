<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Forest验证码</title>
    <style>
        @import url('https://fonts.googleapis.com/css2?family=Poppins:wght@300;400;500;700&display=swap');
        
        body {
            font-family: 'Poppins', Arial, sans-serif;
            background-color: #f0f8ff;
            margin: 0;
            padding: 0;
            color: #333;
        }
        .container {
            max-width: 600px;
            margin: 40px auto;
            background-color: #ffffff;
            border-radius: 16px;
            overflow: hidden;
            box-shadow: 0 10px 20px rgba(0, 0, 0, 0.1);
        }
        .header {
            background-color: #4CAF50;
            padding: 30px 20px;
            text-align: center;
            position: relative;
            overflow: hidden;
        }
        .header::before {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
            bottom: 0;
            background: 
                radial-gradient(circle at 20% 80%, rgba(255,255,255,0.4) 0%, rgba(255,255,255,0.4) 5%, transparent 5%),
                radial-gradient(circle at 80% 60%, rgba(255,255,255,0.4) 0%, rgba(255,255,255,0.4) 8%, transparent 8%),
                radial-gradient(circle at 50% 30%, rgba(255,255,255,0.4) 0%, rgba(255,255,255,0.4) 10%, transparent 10%);
        }
        .sun {
            position: absolute;
            top: 20px;
            right: 30px;
            width: 40px;
            height: 40px;
            background: #FFD700;
            border-radius: 50%;
            box-shadow: 0 0 20px #FFD700;
        }
        .logo {
            display: none;
        }
        .company-name {
            color: #ffffff;
            font-size: 32px;
            font-weight: 700;
            margin: 0;
            text-shadow: 2px 2px 4px rgba(0,0,0,0.1);
        }
        .main-content {
            padding: 40px;
            text-align: center;
        }
        .title {
            font-size: 28px;
            font-weight: 500;
            margin-bottom: 20px;
            color: #2E7D32;
        }
        .code {
            background-color: #E8F5E9;
            border: 2px solid #4CAF50;
            border-radius: 12px;
            padding: 20px;
            font-size: 36px;
            letter-spacing: 6px;
            font-weight: 700;
            display: inline-block;
            margin: 20px 0;
            color: #2E7D32;
            box-shadow: 0 4px 8px rgba(0,0,0,0.1);
        }
        .instructions {
            font-size: 18px;
            line-height: 1.6;
            margin-bottom: 20px;
        }
        .copy-instruction {
            font-size: 16px;
            color: #666;
            margin-top: 15px;
        }
        .cta-button {
            display: inline-block;
            background-color: #4CAF50;
            color: white;
            text-decoration: none;
            padding: 14px 28px;
            border-radius: 8px;
            font-weight: 500;
            margin-top: 25px;
            transition: all 0.3s ease;
            box-shadow: 0 4px 8px rgba(0,0,0,0.1);
        }
        .cta-button:hover {
            background-color: #45a049;
            transform: translateY(-2px);
            box-shadow: 0 6px 12px rgba(0,0,0,0.15);
        }
        .footer {
            background-color: #f5f5f5;
            text-align: center;
            padding: 25px;
            font-size: 16px;
            color: #666;
        }
        a {
            color: #4CAF50;
            text-decoration: none;
            transition: color 0.3s ease;
        }
        a:hover {
            color: #45a049;
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="sun"></div>
            <h1 class="company-name">{{$name}}</h1>
        </div>
        <div class="main-content">
            <h2 class="title">验证您的电子邮件</h2>
            <p class="instructions">请使用以下验证码激活您的帐户：</p>
            <div class="code">{{$code}}</div>
            <p class="copy-instruction">长按（移动设备）或双击（电脑）上方的验证码即可选中并复制</p>
            <p class="instructions">
                此验证码将在 5 分钟后过期。<br>
                如果您没有请求此验证码，请忽略此邮件。
            </p>
            <a href="{{$url}}" class="cta-button">返回{{$name}}</a>
        </div>
        <div class="footer">
            &copy; 2025 <a href="{{$url}}">{{$name}}</a>. 保留所有权利。
        </div>
    </div>
</body>
</html>