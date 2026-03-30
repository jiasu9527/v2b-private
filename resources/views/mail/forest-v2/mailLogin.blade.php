<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{$name}}登录确认</title>
    <style>
        @import url('https://fonts.googleapis.com/css2?family=Roboto:wght@300;400;500;700&display=swap');
        
        body {
            font-family: 'Roboto', Arial, sans-serif;
            background-color: #f0f4f0;
            background-image: url("data:image/svg+xml,%3Csvg width='60' height='60' viewBox='0 0 60 60' xmlns='http://www.w3.org/2000/svg'%3E%3Cg fill='none' fill-rule='evenodd'%3E%3Cg fill='%239ABC66' fill-opacity='0.1'%3E%3Cpath d='M36 34v-4h-2v4h-4v2h4v4h2v-4h4v-2h-4zm0-30V0h-2v4h-4v2h4v4h2V6h4V4h-4zM6 34v-4H4v4H0v2h4v4h2v-4h4v-2H6zM6 4V0H4v4H0v2h4v4h2V6h4V4H6z'/%3E%3C/g%3E%3C/g%3E%3C/svg%3E");
            margin: 0;
            padding: 0;
            color: #333;
        }
        .container {
            max-width: 600px;
            margin: 20px auto;
            background-color: #ffffff;
            border-radius: 8px;
            overflow: hidden;
            box-shadow: 0 10px 25px rgba(0, 0, 0, 0.1);
            border: 1px solid #e0e0e0;
        }
        .header {
            background-color: #5eb21d;
            background-image: linear-gradient(135deg, #5eb21d 0%, #4a8f17 100%);
            padding: 40px 20px; 
            text-align: center;
        }
        .company-name {
            color: #ffffff;
            font-size: 28px;
            font-weight: 700;
            margin: 0;
            text-shadow: 1px 1px 2px rgba(0,0,0,0.1);
        }
        .main-content {
            padding: 40px 30px;
            text-align: center;
        }
        .title {
            font-size: 24px;
            font-weight: 500;
            margin-bottom: 30px;
            color: #2f7737;
            position: relative;
        }
        .title::after {
            content: '';
            display: block;
            width: 50px;
            height: 3px;
            background-color: #5eb21d;
            margin: 15px auto 0;
        }
        .instructions {
            font-size: 16px;
            line-height: 1.6;
            margin-bottom: 20px;
        }
        .cta-button {
            display: inline-block;
            background-color: #5eb21d;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            color: white;
            text-decoration: none;
            padding: 12px 24px;
            border-radius: 50px;
            font-weight: 700;
            margin-top: 20px;
            transition: all 0.3s ease;
        }
        .cta-button:hover {
            background-color: #4a8f17;
            box-shadow: 0 6px 8px rgba(0, 0, 0, 0.15);
            transform: translateY(-2px);
        }
        .footer {
            background-color: #f5f5f5;
            text-align: center;
            padding: 20px;
            font-size: 14px;
            color: #666;
            border-top: 1px solid #e0e0e0;
        }
        a {
            color: #5eb21d;
            text-decoration: none;
        }
        a:hover {
            text-decoration: underline;
        }
        @media (max-width: 600px) {
            .container {
                margin: 10px;
                width: calc(100% - 20px);
            }
            .main-content {
                padding: 30px 20px;
            }
            .title {
                font-size: 22px;
            }
            .instructions {
                font-size: 14px;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1 class="company-name">{{$name}}</h1>
        </div>
        <div class="main-content">
            <h2 class="title">您正在登入到{{$name}}</h2>
            <p class="instructions">
                您正在登入到{{$name}}，请在 5 分钟内点击下方链接进行登入。如果您未授权该登入请求，请无视。
            </p>
            <a href="{{$link}}" class="cta-button">立即登录</a>
            <p class="instructions" style="margin-top: 20px;">
                如果你没有提出此请求，请忽略该邮件。
            </p>
        </div>
        <div class="footer">
            &copy; 2025 <a href="{{$url}}">{{$name}}</a>. 保留所有权利。
        </div>
    </div>
</body>
</html>