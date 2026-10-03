using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Net.Http.Json;
using System.Text;
using System.Text.Json;
using System.Threading.Tasks;
using Jellyfin.Api;
using MediaBrowser.Common.Net;
using MediaBrowser.Controller.Configuration;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.DependencyInjection;
using Xunit;

namespace Jellyfin.Server.Integration.Tests.Controllers;

public sealed class RemovedFeaturesTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;
    private static string? _accessToken;

    public RemovedFeaturesTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    public static IEnumerable<object[]> RemovedRequests()
    {
        var paths = new[]
        {
            "/LiveTv", "/livetv/", "/LiveTv/Info", "/LiveTv/Channels", "/LiveTv/Programs",
            "/LiveTv/TunerHosts", "/LiveTv/Recordings/123", "/LiveTv/Timers", "/LiveTv/SeriesTimers",
            "/Channels", "/CHANNELS/Items", "/Dlna", "/dlna/profiles",
            "/System/Configuration/livetv", "/system/configuration/LIVETV/"
        };
        var methods = new[] { "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD" };
        var languages = new[]
        {
            ("en-US", "en-US", "Discovery, live TV, recordings and channels are not supported."),
            ("zh-CN", "zh-CN", "不支持设备发现、直播电视、录制和频道。"),
            ("zh-TW", "zh-TW", "不支援裝置探索、直播電視、錄製與頻道。"),
            ("ja", "ja-JP", "デバイス検出、ライブテレビ、録画、チャンネルには対応していません。"),
            ("fr-FR", "en-US", "Discovery, live TV, recordings and channels are not supported."),
            (string.Empty, "zh-CN", "不支持设备发现、直播电视、录制和频道。")
        };
        foreach (var path in paths)
        {
            foreach (var method in methods)
            {
                foreach (var (header, culture, message) in languages)
                {
                    yield return new object[] { path, method, header, culture, message };
                }
            }
        }
    }

    [Theory]
    [MemberData(nameof(RemovedRequests))]
    public async Task RemovedSurface_AllMethods_ReturnUnsupportedWithoutConfigurationChanges(string path, string method, string header, string culture, string message)
    {
        using var client = _factory.CreateClient();
        if (path.StartsWith("/System/", System.StringComparison.OrdinalIgnoreCase))
        {
            client.DefaultRequestHeaders.AddAuthHeader(_accessToken ??= await AuthHelper.CompleteStartupAsync(client));
        }

        var configuration = _factory.Services.GetRequiredService<IServerConfigurationManager>();
        var before = JsonSerializer.Serialize(configuration.GetConfiguration("livetv"));
        using var request = new HttpRequestMessage(new HttpMethod(method), path);
        request.Content = new StringContent("{\"TunerHosts\":[{\"Type\":\"m3u\",\"Url\":\"http://127.0.0.1:1/retired\"}]}", Encoding.UTF8, "application/json");
        if (header.Length > 0)
        {
            request.Headers.TryAddWithoutValidation("Accept-Language", header);
        }

        using var response = await client.SendAsync(request, TestContext.Current.CancellationToken);

        Assert.Equal(HttpStatusCode.NotImplemented, response.StatusCode);
        Assert.Equal("no-store", response.Headers.CacheControl?.ToString());
        Assert.Equal(new[] { culture }, response.Content.Headers.ContentLanguage);
        Assert.Equal("application/json", response.Content.Headers.ContentType?.MediaType);
        if (method == "HEAD")
        {
            Assert.Empty(await response.Content.ReadAsByteArrayAsync(TestContext.Current.CancellationToken));
        }
        else
        {
            using var document = JsonDocument.Parse(await response.Content.ReadAsStringAsync(TestContext.Current.CancellationToken));
            var error = document.RootElement.GetProperty("error");
            Assert.Equal("feature_removed", error.GetProperty("code").GetString());
            Assert.Equal(message, error.GetProperty("message").GetString());
            Assert.Empty(error.GetProperty("details").EnumerateObject());
            Assert.False(string.IsNullOrWhiteSpace(error.GetProperty("traceId").GetString()));
        }

        Assert.Equal(before, JsonSerializer.Serialize(configuration.GetConfiguration("livetv")));
    }

    [Fact]
    public async Task AuthenticatedTunerAndConfigurationWrites_RemainUnsupported()
    {
        using var client = _factory.CreateClient();
        client.DefaultRequestHeaders.AddAuthHeader(_accessToken ??= await AuthHelper.CompleteStartupAsync(client));
        var configuration = _factory.Services.GetRequiredService<IServerConfigurationManager>();
        var before = JsonSerializer.Serialize(configuration.GetConfiguration("livetv"));
        foreach (var path in new[] { "/LiveTv/TunerHosts", "/System/Configuration/livetv" })
        {
            using var response = await client.PostAsJsonAsync(path, new { TunerHosts = new[] { new { Type = "m3u", Url = "http://127.0.0.1:1/retired" } } }, TestContext.Current.CancellationToken);
            Assert.Equal(HttpStatusCode.NotImplemented, response.StatusCode);
            Assert.Equal(before, JsonSerializer.Serialize(configuration.GetConfiguration("livetv")));
        }
    }

    [Theory]
    [InlineData("GET")]
    [InlineData("POST")]
    public async Task NamedConfiguration_Unauthenticated_RetainsAuthorization(string method)
    {
        using var client = _factory.CreateClient();
        using var request = new HttpRequestMessage(new HttpMethod(method), "/System/Configuration/livetv");
        request.Content = new StringContent("{}", Encoding.UTF8, "application/json");
        using var response = await client.SendAsync(request, TestContext.Current.CancellationToken);
        Assert.Equal(HttpStatusCode.Unauthorized, response.StatusCode);
    }

    [Theory]
    [InlineData("/LiveTvs/Info", HttpStatusCode.NotFound)]
    [InlineData("/ChannelsExtra", HttpStatusCode.NotFound)]
    [InlineData("/DlnaExtra", HttpStatusCode.NotFound)]
    [InlineData("/System/Configuration/network", HttpStatusCode.Unauthorized)]
    [InlineData("/System/Info/Public?next=/LiveTv", HttpStatusCode.OK)]
    public async Task SimilarPathsAndQuery_DoNotMatchRetiredFeatures(string path, HttpStatusCode expected)
    {
        using var client = _factory.CreateClient();
        using var response = await client.GetAsync(path, TestContext.Current.CancellationToken);
        Assert.Equal(expected, response.StatusCode);
    }

    [Theory]
    [InlineData("/LiveTv/Info")]
    [InlineData("/Channels/Items")]
    [InlineData("/Dlna/Profiles")]
    public async Task RemovedProbe_DeniedRemoteAddress_RetainsNetworkRestriction(string path)
    {
        using var client = _factory.CreateClient();
        var configuration = _factory.Services.GetRequiredService<IServerConfigurationManager>().GetNetworkConfiguration();
        var previous = configuration.EnableRemoteAccess;
        try
        {
            configuration.EnableRemoteAccess = false;
            var context = await _factory.Server.SendAsync(
                context =>
                {
                    context.Request.Method = HttpMethods.Get;
                    context.Request.Path = path;
                    context.Connection.RemoteIpAddress = IPAddress.Parse("203.0.113.42");
                    context.Connection.LocalIpAddress = IPAddress.Loopback;
                },
                TestContext.Current.CancellationToken);
            Assert.Equal(StatusCodes.Status503ServiceUnavailable, context.Response.StatusCode);
            Assert.NotEqual("no-store", context.Response.Headers.CacheControl.ToString());
        }
        finally
        {
            configuration.EnableRemoteAccess = previous;
        }
    }

    [Fact]
    public void ApiAssembly_DoesNotContainRetiredControllersOrDedicatedDtos()
    {
        Assert.DoesNotContain(typeof(BaseJellyfinApiController).Assembly.GetTypes(), type => type.Name is "LiveTvController" or "ChannelsController" or "GetProgramsDto" or "SetChannelMappingDto");
    }

    [Fact]
    public async Task OpenApi_DoesNotAdvertiseRemovedOperationsOrDtos()
    {
        using var client = _factory.CreateClient();
        using var response = await client.GetAsync("/api-docs/openapi.json", TestContext.Current.CancellationToken);
        response.EnsureSuccessStatusCode();
        using var document = JsonDocument.Parse(await response.Content.ReadAsStringAsync(TestContext.Current.CancellationToken));
        foreach (var path in document.RootElement.GetProperty("paths").EnumerateObject())
        {
            Assert.False(path.Name.StartsWith("/LiveTv", System.StringComparison.OrdinalIgnoreCase));
            Assert.False(path.Name.StartsWith("/Channels", System.StringComparison.OrdinalIgnoreCase));
        }

        var schemas = document.RootElement.GetProperty("components").GetProperty("schemas");
        Assert.False(schemas.TryGetProperty("GetProgramsDto", out _));
        Assert.False(schemas.TryGetProperty("SetChannelMappingDto", out _));
    }
}
