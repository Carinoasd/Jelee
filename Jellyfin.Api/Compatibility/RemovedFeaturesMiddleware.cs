using System;
using System.Collections.Generic;
using System.Threading.Tasks;
using MediaBrowser.Model.Globalization;
using Microsoft.AspNetCore.Http;

namespace Jelee.Api.Compatibility;

/// <summary>
/// Rejects retired discovery, live TV, recording and channel HTTP surfaces.
/// </summary>
public sealed class RemovedFeaturesMiddleware
{
    private readonly RequestDelegate _next;

    /// <summary>
    /// Initializes a new instance of the <see cref="RemovedFeaturesMiddleware"/> class.
    /// </summary>
    /// <param name="next">The next request delegate.</param>
    public RemovedFeaturesMiddleware(RequestDelegate next)
    {
        _next = next;
    }

    /// <summary>
    /// Returns an explicit unsupported-feature response without invoking feature services.
    /// </summary>
    /// <param name="context">The request context.</param>
    /// <param name="localization">The four-language resource provider.</param>
    /// <returns>The response task.</returns>
    public async Task InvokeAsync(HttpContext context, ILocalizationManager localization)
    {
        var path = context.Request.Path;
        if (!path.StartsWithSegments("/LiveTv", StringComparison.OrdinalIgnoreCase)
            && !path.StartsWithSegments("/Channels", StringComparison.OrdinalIgnoreCase)
            && !path.StartsWithSegments("/Dlna", StringComparison.OrdinalIgnoreCase)
            && !path.StartsWithSegments("/System/Configuration/livetv", StringComparison.OrdinalIgnoreCase))
        {
            await _next(context).ConfigureAwait(false);
            return;
        }

        context.Response.StatusCode = StatusCodes.Status501NotImplemented;
        context.Response.Headers.CacheControl = "no-store";
        context.Response.ContentType = "application/json";
        if (HttpMethods.IsHead(context.Request.Method))
        {
            return;
        }

        await context.Response.WriteAsJsonAsync(
            new
            {
                error = new
                {
                    code = "feature_removed",
                    message = localization.GetLocalizedString("FeatureRemoved"),
                    details = new Dictionary<string, string>(),
                    traceId = context.TraceIdentifier
                }
            },
            cancellationToken: context.RequestAborted).ConfigureAwait(false);
    }
}
