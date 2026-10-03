using System;
using System.IO;
using Jellyfin.LiveTv.Recordings;
using Jellyfin.LiveTv.Timers;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Controller.Configuration;
using MediaBrowser.Controller.Dto;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.LiveTv;
using MediaBrowser.Controller.MediaEncoding;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.IO;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging.Abstractions;
using Moq;
using Xunit;

namespace Jellyfin.Server.Integration.Tests;

public sealed class RecordingStartupRemovalTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;

    public RecordingStartupRemovalTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    [Fact]
    public void Startup_DoesNotRegisterRecordingHosts()
    {
        using var client = _factory.CreateClient();
        var hostedServices = _factory.Services.GetServices<IHostedService>();

        Assert.DoesNotContain(hostedServices, service => service.GetType().Name is "RecordingsHost" or "RecordingNotifier");
        Assert.NotNull(_factory.Services.GetRequiredService<IRecordingsManager>());
    }

    [Fact]
    public void RecordingAssembly_DoesNotContainRetiredHosts()
    {
        Assert.DoesNotContain(typeof(RecordingsManager).Assembly.GetTypes(), type => type.Name is "RecordingsHost" or "RecordingNotifier");
    }

    [Fact]
    public void ConfigurationUpdate_DoesNotCreateRecordingFolders()
    {
        var config = new Mock<IServerConfigurationManager>();
        var paths = new Mock<IApplicationPaths>();
        var dataPath = Path.Combine(Path.GetTempPath(), "jelee-unused-recording-" + Guid.NewGuid());
        paths.SetupGet(p => p.DataPath).Returns(dataPath);
        config.SetupGet(c => c.CommonApplicationPaths).Returns(paths.Object);
        var library = new Mock<ILibraryManager>();
        var timers = new TimerManager(NullLogger<TimerManager>.Instance, config.Object);
        var seriesTimers = new SeriesTimerManager(NullLogger<SeriesTimerManager>.Instance, config.Object);
        var metadata = new RecordingsMetadataManager(NullLogger<RecordingsMetadataManager>.Instance, config.Object, library.Object);
        using var manager = new RecordingsManager(
            NullLogger<RecordingsManager>.Instance,
            config.Object,
            Mock.Of<System.Net.Http.IHttpClientFactory>(),
            Mock.Of<IFileSystem>(),
            library.Object,
            Mock.Of<ILibraryMonitor>(),
            Mock.Of<IProviderManager>(),
            Mock.Of<IMediaEncoder>(),
            Mock.Of<IMediaSourceManager>(),
            Mock.Of<IStreamHelper>(),
            timers,
            seriesTimers,
            metadata);
        config.VerifyAdd(c => c.NamedConfigurationUpdated += It.IsAny<EventHandler<ConfigurationUpdateEventArgs>>(), Times.Never());
        config.Invocations.Clear();
        paths.Invocations.Clear();

        config.Raise(c => c.NamedConfigurationUpdated += null, new ConfigurationUpdateEventArgs("livetv", new MediaBrowser.Model.LiveTv.LiveTvOptions()));

        config.VerifyNoOtherCalls();
        library.VerifyNoOtherCalls();
        Assert.False(Directory.Exists(dataPath));
    }
}
