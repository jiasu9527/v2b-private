<!DOCTYPE html>
<html>

<head>
    <link rel="stylesheet" href="/theme/{{$theme}}/assets/components.chunk.css?v={{$version}}">
    <link rel="stylesheet" href="/theme/{{$theme}}/assets/umi.css?v={{$version}}">
    <link rel="stylesheet" href="/assets/invite-campaign-common.css?v={{$version}}">
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{$title}} - 邀请活动任务</title>
    <script>
        window.InviteCampaignUserPage = {
            apiBase: '/api/v1',
            loginPath: '/#/login',
            dashboardPath: '/#/dashboard',
            invitePath: '/#/invite',
            planPath: '/#/plan',
            orderPathPrefix: '/#/order/'
        };
    </script>
</head>

<body class="campaign-page">
<div class="campaign-shell">
    <div class="campaign-hero">
        <div>
            <h1>邀请活动任务</h1>
            <p>完成邀请任务后，购买绑定套餐时自动使用活动抵扣。</p>
        </div>
        <div class="campaign-actions">
            <a class="btn btn-alt-secondary" href="/#/invite">返回邀请页</a>
            <a class="btn btn-alt-secondary" href="/#/dashboard">返回仪表盘</a>
        </div>
    </div>
    <div id="invite-campaign-user-app" class="campaign-card campaign-loading">正在加载活动任务...</div>
</div>
<script src="/assets/user-invite-campaign-page.js?v={{$version}}"></script>
</body>

</html>
