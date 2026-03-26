<!DOCTYPE html>
<html>

<head>
    <link rel="stylesheet" href="/assets/admin/components.chunk.css?v={{$asset_version ?? $version}}">
    <link rel="stylesheet" href="/assets/admin/umi.css?v={{$asset_version ?? $version}}">
    <link rel="stylesheet" href="/assets/admin/custom.css?v={{$asset_version ?? $version}}">
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{$title}}</title>
    <!-- <link rel="stylesheet" href="https://fonts.googleapis.com/css?family=Nunito+Sans:300,400,400i,600,700"> -->
    <script>window.routerBase = "/";</script>
    <script>
        window.settings = {
            title: '{{$title}}',
            theme: {
                sidebar: '{{$theme_sidebar}}',
                header: '{{$theme_header}}',
                color: '{{$theme_color}}',
            },
            version: '{{$asset_version ?? $version}}',
            background_url: '{{$background_url}}',
            logo: '{{$logo}}',
            secure_path: '{{$secure_path}}'
        }
    </script>
</head>

<body>
<div id="root"></div>
<script src="/assets/admin/vendors.async.js?v={{$asset_version ?? $version}}"></script>
<script src="/assets/admin/components.async.js?v={{$asset_version ?? $version}}"></script>
<script src="/assets/admin/umi.js?v={{$asset_version ?? $version}}"></script>
@if (file_exists(public_path('/assets/admin/custom.js')))
    <script src="/assets/admin/custom.js?v={{$asset_version ?? $version}}"></script>
@endif
</body>

</html>
