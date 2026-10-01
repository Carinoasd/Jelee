using System.Globalization;
using System.Net;
using System.Net.Http;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Localization;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;
using Moq;
using Xunit;

namespace Jellyfin.Server.Integration.Tests.Middleware;

public sealed class FourLocaleRequestCultureTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;

    public FourLocaleRequestCultureTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    [Theory]
    [InlineData("", "zh-CN")]
    [InlineData("  ", "zh-CN")]
    [InlineData("fr-FR", "en-US")]
    [InlineData("zh-CN", "zh-CN")]
    [InlineData("zh-TW", "zh-TW")]
    [InlineData("ja-JP", "ja-JP")]
    [InlineData("en-US", "en-US")]
    [InlineData("zh-Hant", "zh-TW")]
    [InlineData("zh-HK", "zh-TW")]
    [InlineData("zh-Hans-SG", "zh-CN")]
    [InlineData("zh", "zh-CN")]
    [InlineData("ja", "ja-JP")]
    [InlineData("en-GB", "en-US")]
    [InlineData("ZH-tW", "zh-TW")]
    [InlineData("en-US;q=0.4,ja-JP;q=0.9", "ja-JP")]
    [InlineData("fr-FR;q=1,zh-TW;q=0.8", "zh-TW")]
    [InlineData("ja;q=0.8,en;q=0.8", "ja-JP")]
    [InlineData("en;q=0.8,ja;q=0.8", "en-US")]
    [InlineData("ja;q=0,en;q=0.5", "en-US")]
    [InlineData("*", "zh-CN")]
    [InlineData("zh-CN;q=0,*;q=0.5", "zh-TW")]
    [InlineData("*;q=0,ja-JP;q=0.5", "ja-JP")]
    [InlineData("en-US;q=0,en;q=1,ja;q=0.4", "ja-JP")]
    [InlineData("en;q=NaN,ja;q=0.5", "ja-JP")]
    [InlineData("en;q=1.1,ja;q=0.5", "ja-JP")]
    [InlineData("en;q=-1,ja;q=0.5", "ja-JP")]
    [InlineData("en;q=0.0001,ja;q=0.5", "ja-JP")]
    [InlineData("en;q=.9,ja;q=0.5", "ja-JP")]
    [InlineData("invalid_@;q=1", "en-US")]
    [InlineData("ja;q=0", "en-US")]
    [InlineData("en;q=0.3;q=1,ja;q=0.5", "ja-JP")]
    [InlineData("ja ; Q = 0.500, en;q=0.4", "ja-JP")]
    public async Task HeaderNegotiation_SetsCanonicalContentLanguage(string header, string expected)
    {
        using var client = _factory.CreateClient();
        using var request = new HttpRequestMessage(HttpMethod.Get, "/System/Info/Public");
        request.Headers.TryAddWithoutValidation("Accept-Language", header);
        using var response = await client.SendAsync(request, TestContext.Current.CancellationToken);

        Assert.Equal(HttpStatusCode.OK, response.StatusCode);
        Assert.Equal(new[] { expected }, response.Content.Headers.ContentLanguage);
    }

    [Fact]
    public async Task OversizedHeader_FallsBackToEnglish()
    {
        using var client = _factory.CreateClient();
        using var request = new HttpRequestMessage(HttpMethod.Get, "/System/Info/Public");
        request.Headers.TryAddWithoutValidation("Accept-Language", new string('a', 8193));
        using var response = await client.SendAsync(request, TestContext.Current.CancellationToken);

        Assert.Equal(HttpStatusCode.OK, response.StatusCode);
        Assert.Equal(new[] { "en-US" }, response.Content.Headers.ContentLanguage);
    }

    [Theory]
    [InlineData("fr-FR")]
    [InlineData("invalid_@;q=1")]
    [InlineData("ja;q=0")]
    public async Task EnglishFallback_DoesNotLogUnsupportedCulture(string header)
    {
        using var client = _factory.CreateClient();
        var logger = new Mock<ILogger>();
        var loggerFactory = new Mock<ILoggerFactory>();
        loggerFactory.Setup(factory => factory.CreateLogger(It.IsAny<string>())).Returns(logger.Object);
        var options = _factory.Services.GetRequiredService<IOptions<RequestLocalizationOptions>>();
        var middleware = new RequestLocalizationMiddleware(
            _ =>
            {
                Assert.Equal("en-US", CultureInfo.CurrentUICulture.Name);
                return Task.CompletedTask;
            },
            options,
            loggerFactory.Object);
        var context = new DefaultHttpContext();
        context.Request.Headers.AcceptLanguage = header;

        await middleware.Invoke(context);

        Assert.Equal("en-US", context.Response.Headers.ContentLanguage.ToString());
        logger.VerifyNoOtherCalls();
    }

    [Fact]
    public async Task QueryCulture_StillOverridesHeader()
    {
        using var client = _factory.CreateClient();
        using var request = new HttpRequestMessage(HttpMethod.Get, "/System/Info/Public?culture=zh-TW&ui-culture=zh-TW");
        request.Headers.AcceptLanguage.ParseAdd("ja-JP");
        using var response = await client.SendAsync(request, TestContext.Current.CancellationToken);

        Assert.Equal(HttpStatusCode.OK, response.StatusCode);
        Assert.Equal(new[] { "zh-TW" }, response.Content.Headers.ContentLanguage);
    }
}
