using System;
using Jellyfin.LiveTv;
using Jellyfin.LiveTv.Extensions;
using MediaBrowser.Controller.Channels;
using MediaBrowser.Controller.LiveTv;
using Microsoft.Extensions.DependencyInjection;
using Xunit;

namespace Jellyfin.Server.Integration.Tests;

public sealed class LiveCoreRegistrationRemovalTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;

    public LiveCoreRegistrationRemovalTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    [Theory]
    [InlineData(typeof(ILiveTvManager))]
    [InlineData(typeof(Lazy<ILiveTvManager>))]
    [InlineData(typeof(ILiveTvService))]
    [InlineData(typeof(LiveTvDtoService))]
    public void RetiredCore_IsNotRegisteredOrResolvable(Type serviceType)
    {
        var services = new ServiceCollection();
        services.AddLiveTvServices();
        Assert.DoesNotContain(services, descriptor => descriptor.ServiceType == serviceType);

        using var client = _factory.CreateClient();
        Assert.Null(_factory.Services.GetService(serviceType));
        Assert.False(_factory.Services.GetRequiredService<IServiceProviderIsService>().IsService(serviceType));
    }

    [Theory]
    [InlineData(typeof(IRecordingsManager), "IGuideManager")]
    [InlineData(typeof(LiveTvDtoService), "GuideManager")]
    public void CompiledAssembly_DoesNotContainGuideCore(Type assemblyMarker, string name)
    {
        Assert.DoesNotContain(assemblyMarker.Assembly.GetTypes(), type => type.Name == name);
    }

    [Fact]
    public void Startup_RetainsResolvableLibraryDependencies()
    {
        using var client = _factory.CreateClient();
        Assert.NotNull(_factory.Services.GetRequiredService<IChannelManager>());
        Assert.NotNull(_factory.Services.GetRequiredService<IRecordingsManager>());
        Assert.NotNull(_factory.Services.GetRequiredService<IListingsManager>());
        Assert.NotNull(_factory.Services.GetRequiredService<ITunerHostManager>());
    }
}
