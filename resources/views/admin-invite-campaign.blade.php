<!DOCTYPE html>
<html>

<head>
    <link rel="stylesheet" href="/assets/admin/components.chunk.css?v={{$version}}">
    <link rel="stylesheet" href="/assets/admin/umi.css?v={{$version}}">
    <link rel="stylesheet" href="/assets/admin/custom.css?v={{$version}}">
    <link rel="stylesheet" href="/assets/invite-campaign-common.css?v={{$version}}">
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{$title}} - 任务管理</title>
    <script>
        window.InviteCampaignAdminPage = {
            apiBase: '/api/v1',
            securePath: '{{$secure_path}}',
            loginPath: '/{{$secure_path}}',
            backPath: '/{{$secure_path}}'
        };
    </script>
</head>

<body class="campaign-page">
<div id="invite-campaign-admin-app" class="campaign-shell">
    <div class="campaign-card campaign-loading">正在加载任务列表...</div>
</div>
<script src="/assets/admin-invite-campaign-page.js?v={{$version}}"></script>
</body>

</html>
