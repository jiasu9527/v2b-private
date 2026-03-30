<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Forest流量通知</title>
    <style>
        @import url('https://fonts.googleapis.com/css2?family=Roboto:wght@300;400;500;700&display=swap');
        
        body {
            font-family: 'Roboto', Arial, sans-serif;
            background-color: #f0f4f0;
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
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        .header {
            background-color: #5eb21d;
            padding: 30px 20px; /* Updated padding */
            text-align: center;
        }
        
        .company-name {
            color: #ffffff;
            font-size: 28px;
            font-weight: 700;
            margin: 0;
        }
        .main-content {
            padding: 40px;
            text-align: center;
        }
        .title {
            font-size: 24px;
            font-weight: 500;
            margin-bottom: 20px;
            color: #2f7737;
        }
        .code {
            background-color: #E8F5E9;
            border: 2px solid #5eb21d;
            border-radius: 8px;
            padding: 16px;
            font-size: 32px;
            letter-spacing: 4px;
            font-weight: 700;
            display: inline-block;
            margin: 20px 0;
            color: #2f7737;
        }
        .instructions {
            font-size: 16px;
            line-height: 1.6;
            margin-bottom: 20px;
        }
        .cta-button {
            display: inline-block;
            background-color: #5eb21d;
            color: white;
            text-decoration: none;
            padding: 12px 24px;
            border-radius: 4px;
            font-weight: 500;
            margin-top: 20px;
            transition: background-color 0.3s, transform 0.2s;
        }
        .cta-button:hover {
            background-color: #4a8f17;
            transform: translateY(-2px);
        }
        .footer {
            background-color: #f5f5f5;
            text-align: center;
            padding: 20px;
            font-size: 14px;
            color: #666;
        }
        a {
            color: #5eb21d;
            text-decoration: none;
        }
        a:hover {
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <!--<img src="https://forest-cn.com/img/logo.png" alt="Forest Logo" class="logo">-->  <!--Removed logo image-->
            <h1 class="company-name">{{$name}}</h1>
        </div>
        <div class="main-content">
            <h2 class="title">流量通知</h2>
            <p class="instructions">尊敬的用户您好！</p>
            <p class="instructions">
                你的流量已经使用80%。为了不造成使用上的影响请合理安排流量的使用。
            </p>
            <a href="{{$url}}" class="cta-button">查看详情</a>
        </div>
        <div class="footer">
            &copy; 2025 <a href="{{$url}}">{{$name}}</a>. 保留所有权利。
        </div>
    </div>
</body>
</html>