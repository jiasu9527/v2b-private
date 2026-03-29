<?php

namespace App\Http\Middleware;

use Closure;

class RequestLog
{
    /**
     * Handle an incoming request.
     *
     * @param \Illuminate\Http\Request $request
     * @param \Closure $next
     * @return mixed
     */
    public function handle($request, Closure $next)
    {
        if ($request->method() === 'POST') {
            try {
                $path = $request->path();
                info("POST {$path}");
            } catch (\Throwable $e) {
                error_log(sprintf(
                    'RequestLog failed for [%s] %s: %s',
                    $request->method(),
                    $request->path(),
                    $e->getMessage()
                ));
            }
        };
        return $next($request);
    }
}
