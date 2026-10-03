using System;
using System.Linq;
using System.Net.Http.Json;
using System.Threading.Tasks;
using Jellyfin.Extensions.Json;
using Jellyfin.LiveTv.Listings;
using Jellyfin.LiveTv.Recordings;
using Jellyfin.LiveTv.TunerHosts;
using MediaBrowser.Model.Tasks;
using Microsoft.Extensions.DependencyInjection;
using Xunit;

namespace Jellyfin.Server.Integration.Tests;

public sealed class LiveFeatureActorsRemovalTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;

    public LiveFeatureActorsRemovalTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    [Theory]
    [InlineData("RefreshGuideScheduledTask")]
    [InlineData("RefreshChannelsScheduledTask")]
    [InlineData("ChannelPostScanTask")]
    [InlineData("LiveTvMediaSourceProvider")]
    [InlineData("ChannelDynamicMediaSourceProvider")]
    [InlineData("ChannelImageProvider")]
    public void CompiledAssembly_DoesNotContainRetiredActor(string name)
    {
        Assert.DoesNotContain(typeof(RecordingsManager).Assembly.GetTypes(), type => type.Name == name);
    }

    [Fact]
    public void Startup_DoesNotRegisterGuideOrChannelTasks()
    {
        using var client = _factory.CreateClient();
        var tasks = _factory.Services.GetRequiredService<ITaskManager>().ScheduledTasks;
        Assert.NotEmpty(tasks);
        Assert.DoesNotContain(tasks, task => task.ScheduledTask.Key is "RefreshGuide" or "RefreshInternetChannels");
    }

    [Fact]
    public async Task ScheduledTasksApi_DoesNotAdvertiseRetiredTasks()
    {
        using var client = _factory.CreateClient();
        client.DefaultRequestHeaders.AddAuthHeader(await AuthHelper.CompleteStartupAsync(client));
        using var response = await client.GetAsync("/ScheduledTasks", TestContext.Current.CancellationToken);
        response.EnsureSuccessStatusCode();
        var tasks = await response.Content.ReadFromJsonAsync<TaskInfo[]>(JsonDefaults.Options, TestContext.Current.CancellationToken);
        Assert.NotNull(tasks);
        Assert.NotEmpty(tasks);
        foreach (var task in tasks)
        {
            Assert.DoesNotContain(task.Key, new[] { "RefreshGuide", "RefreshInternetChannels" });
        }
    }

    [Theory]
    [InlineData(typeof(TunerHostManager))]
    [InlineData(typeof(ListingsManager))]
    public void Producers_DoNotDependOnTaskManager(Type type)
    {
        Assert.DoesNotContain(type.GetConstructors().SelectMany(constructor => constructor.GetParameters()), parameter => parameter.ParameterType == typeof(ITaskManager));
    }
}
