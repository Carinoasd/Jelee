using System.Text.Json;
using System.Threading.Tasks;
using Jelee.Networking.Manager;
using MediaBrowser.Common.Net;
using MediaBrowser.Controller.Configuration;
using MediaBrowser.Model.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Xunit;

namespace Jellyfin.Server.Integration.Tests;

public sealed class DiscoveryRemovalTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;

    public DiscoveryRemovalTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    [Fact]
    public void Startup_WithLegacyPreferenceEnabled_HasNoDiscoveryService()
    {
        using var client = _factory.CreateClient();
        var configuration = _factory.Services.GetRequiredService<IServerConfigurationManager>();
        Assert.True(configuration.GetNetworkConfiguration().AutoDiscovery);
        var hostedServices = _factory.Services.GetServices<IHostedService>();

        Assert.DoesNotContain(hostedServices, service => service.GetType().Name == "AutoDiscoveryHost");
        Assert.DoesNotContain(typeof(NetworkManager).Assembly.GetTypes(), type => type.Name == "AutoDiscoveryHost");
    }

    [Fact]
    public async Task OpenApi_DoesNotAdvertiseDiscoveryResponse()
    {
        using var client = _factory.CreateClient();
        using var response = await client.GetAsync("/api-docs/openapi.json", TestContext.Current.CancellationToken);
        response.EnsureSuccessStatusCode();
        using var document = JsonDocument.Parse(await response.Content.ReadAsStringAsync(TestContext.Current.CancellationToken));

        Assert.False(document.RootElement.GetProperty("components").GetProperty("schemas").TryGetProperty("ServerDiscoveryInfo", out _));
    }

    [Fact]
    public void DiscoveryResponse_IsAbsentFromCompiledModels()
    {
        Assert.DoesNotContain(typeof(ServerConfiguration).Assembly.GetTypes(), type => type.Name == "ServerDiscoveryInfo");
    }
}
