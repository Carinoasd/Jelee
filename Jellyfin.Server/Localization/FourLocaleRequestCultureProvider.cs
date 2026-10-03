using System;
using System.Collections.Generic;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Localization;

namespace Jelee.Server.Localization;

/// <summary>
/// Negotiates the four UI locales for the legacy HTTP adapter.
/// </summary>
public sealed class FourLocaleRequestCultureProvider : RequestCultureProvider
{
    private static readonly string[] _locales = ["zh-CN", "zh-TW", "ja-JP", "en-US"];

    /// <inheritdoc />
    public override Task<ProviderCultureResult?> DetermineProviderCultureResult(HttpContext httpContext)
    {
        ArgumentNullException.ThrowIfNull(httpContext);
        var header = httpContext.Request.Headers.AcceptLanguage.ToString();
        if (string.IsNullOrWhiteSpace(header))
        {
            return NullProviderCultureResult;
        }

        return Task.FromResult<ProviderCultureResult?>(new ProviderCultureResult(Negotiate(header)));
    }

    private static string Negotiate(string header)
    {
        if (header.Length > 8192)
        {
            return "en-US";
        }

        var preferences = new List<(string Tag, int Quality, int Order)>();
        var entries = header.Split(',');
        for (var order = 0; order < entries.Length; order++)
        {
            var parts = entries[order].Trim().Split(';');
            var tag = parts[0].Trim().ToLowerInvariant();
            if (!ValidTag(tag) || parts.Length > 2)
            {
                continue;
            }

            var quality = 1000;
            if (parts.Length == 2)
            {
                var parameter = parts[1].Trim().Split('=');
                if (parameter.Length != 2 || !parameter[0].Trim().Equals("q", StringComparison.OrdinalIgnoreCase))
                {
                    continue;
                }

                quality = ParseQuality(parameter[1].Trim());
                if (quality < 0)
                {
                    continue;
                }
            }

            preferences.Add((tag, quality, order));
        }

        var bestLocale = "en-US";
        var bestQuality = -1;
        var bestOrder = int.MaxValue;
        foreach (var locale in _locales)
        {
            var quality = -1;
            var order = int.MaxValue;
            var specificity = -1;
            foreach (var preference in preferences)
            {
                var match = LanguageMatch(preference.Tag, locale);
                if (match > specificity || (match == specificity && match >= 0 && preference.Quality > quality))
                {
                    quality = preference.Quality;
                    order = preference.Order;
                    specificity = match;
                }
            }

            if (specificity >= 0 && quality > 0 && (quality > bestQuality || (quality == bestQuality && order < bestOrder)))
            {
                bestLocale = locale;
                bestQuality = quality;
                bestOrder = order;
            }
        }

        return bestLocale;
    }

    private static bool ValidTag(string tag)
    {
        if (tag == "*")
        {
            return true;
        }

        var parts = tag.Split('-');
        for (var i = 0; i < parts.Length; i++)
        {
            if (parts[i].Length is < 1 or > 8)
            {
                return false;
            }

            foreach (var character in parts[i])
            {
                if ((character < 'a' || character > 'z') && (i == 0 || character < '0' || character > '9'))
                {
                    return false;
                }
            }
        }

        return true;
    }

    private static int ParseQuality(string value)
    {
        var parts = value.Split('.');
        if (parts.Length > 2 || (parts[0] != "0" && parts[0] != "1") || (parts.Length == 2 && parts[1].Length > 3))
        {
            return -1;
        }

        var quality = parts[0] == "1" ? 1000 : 0;
        var weight = 100;
        if (parts.Length == 2)
        {
            foreach (var digit in parts[1])
            {
                if (digit < '0' || digit > '9' || (parts[0] == "1" && digit != '0'))
                {
                    return -1;
                }

                quality += (digit - '0') * weight;
                weight /= 10;
            }
        }

        return quality;
    }

    private static int LanguageMatch(string tag, string locale)
    {
        if (tag == "*")
        {
            return 0;
        }

        if (tag.Equals(locale, StringComparison.OrdinalIgnoreCase))
        {
            return 3;
        }

        var parts = tag.Split('-');
        if (parts[0] == "en" && locale == "en-US")
        {
            return 1;
        }

        if (parts[0] == "ja" && locale == "ja-JP")
        {
            return 1;
        }

        if (parts[0] != "zh" || !locale.StartsWith("zh-", StringComparison.Ordinal))
        {
            return -1;
        }

        var variant = string.Empty;
        for (var i = 1; i < parts.Length; i++)
        {
            switch (parts[i])
            {
                case "hant":
                case "tw":
                case "hk":
                case "mo":
                    variant = "zh-TW";
                    break;
                case "hans":
                case "cn":
                case "sg":
                    variant = "zh-CN";
                    break;
            }
        }

        return variant.Length == 0 ? 1 : (locale == variant ? 2 : -1);
    }
}
