<?php

use App\Services\ThemeService;
use Illuminate\Http\Request;

/*
|--------------------------------------------------------------------------
| Web Routes
|--------------------------------------------------------------------------
|
| Here is where you can register web routes for your application. These
| routes are loaded by the RouteServiceProvider within a group which
| contains the "web" middleware group. Now create something great!
|
*/

Route::get('/', function (Request $request) {
    if (config('v2board.app_url') && config('v2board.safe_mode_enable', 0)) {
        if ($request->server('HTTP_HOST') !== parse_url(config('v2board.app_url'))['host']) {
            abort(403);
        }
    }
    $renderParams = [
        'title' => config('v2board.app_name', 'V2Board'),
        'theme' => config('v2board.frontend_theme', 'default'),
        'version' => config('app.version'),
        'description' => config('v2board.app_description', 'V2Board is best'),
        'logo' => config('v2board.logo')
    ];

    if (!config("theme.{$renderParams['theme']}")) {
        $themeService = new ThemeService($renderParams['theme']);
        $themeService->init();
    }

    $renderParams['theme_config'] = config('theme.' . config('v2board.frontend_theme', 'default'));
    return view('theme::' . config('v2board.frontend_theme', 'default') . '.dashboard', $renderParams);
});

Route::get('/invite-campaign', function (Request $request) {
    if (config('v2board.app_url') && config('v2board.safe_mode_enable', 0)) {
        if ($request->server('HTTP_HOST') !== parse_url(config('v2board.app_url'))['host']) {
            abort(403);
        }
    }

    $renderParams = [
        'title' => config('v2board.app_name', 'V2Board'),
        'theme' => config('v2board.frontend_theme', 'default'),
        'version' => config('app.version'),
        'description' => config('v2board.app_description', 'V2Board is best'),
        'logo' => config('v2board.logo')
    ];
    return view('invite-campaign', $renderParams);
});

$adminSecurePath = config('v2board.secure_path', config('v2board.frontend_admin_path', hash('crc32b', config('app.key'))));
$resolveAdminAssetVersion = function () {
    $baseVersion = config('app.version');
    $files = [
        public_path('assets/admin/vendors.async.js'),
        public_path('assets/admin/components.async.js'),
        public_path('assets/admin/umi.js'),
        public_path('assets/admin/custom.js'),
        public_path('assets/invite-campaign-common.css'),
    ];
    $timestamps = array_map(function ($path) {
        return file_exists($path) ? (int) filemtime($path) : 0;
    }, $files);

    return $baseVersion . '.' . max($timestamps);
};
$renderAdminApp = function () use ($adminSecurePath, $resolveAdminAssetVersion) {
    return view('admin', [
        'title' => config('v2board.app_name', 'V2Board'),
        'theme_sidebar' => config('v2board.frontend_theme_sidebar', 'light'),
        'theme_header' => config('v2board.frontend_theme_header', 'dark'),
        'theme_color' => config('v2board.frontend_theme_color', 'default'),
        'background_url' => config('v2board.frontend_background_url'),
        'version' => config('app.version'),
        'asset_version' => $resolveAdminAssetVersion(),
        'logo' => config('v2board.logo'),
        'secure_path' => $adminSecurePath
    ]);
};

//TODO:: 兼容
Route::get('/' . $adminSecurePath, $renderAdminApp);
Route::get('/' . $adminSecurePath . '/{any}', $renderAdminApp)->where('any', '.*');

if (!empty(config('v2board.subscribe_path'))) {
    Route::get(config('v2board.subscribe_path'), 'V1\\Client\\ClientController@subscribe')->middleware('client');
}
