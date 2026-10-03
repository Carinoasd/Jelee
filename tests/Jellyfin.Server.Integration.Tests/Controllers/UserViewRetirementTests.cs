using System.Linq;
using System.Net.Http.Json;
using System.Threading.Tasks;
using Jellyfin.Data.Enums;
using Jellyfin.Extensions.Json;
using MediaBrowser.Model.Dto;
using MediaBrowser.Model.Querying;
using Xunit;

namespace Jellyfin.Server.Integration.Tests.Controllers;

public sealed class UserViewRetirementTests : IClassFixture<JellyfinApplicationFactory>
{
    private readonly JellyfinApplicationFactory _factory;
    private static string? _accessToken;

    public UserViewRetirementTests(JellyfinApplicationFactory factory)
    {
        _factory = factory;
    }

    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public async Task UserViews_LegacyExternalFlag_DoesNotAddRetiredFolders(bool legacy)
    {
        using var client = _factory.CreateClient();
        client.DefaultRequestHeaders.AddAuthHeader(_accessToken ??= await AuthHelper.CompleteStartupAsync(client));
        var user = await AuthHelper.GetUserDtoAsync(client);
        var path = legacy ? $"/Users/{user.Id}/Views" : "/UserViews";
        string[]? baseline = null;
        foreach (var suffix in new[] { "?includeExternalContent=false", "?includeExternalContent=true", string.Empty })
        {
            using var response = await client.GetAsync(path + suffix, TestContext.Current.CancellationToken);
            response.EnsureSuccessStatusCode();
            var views = await response.Content.ReadFromJsonAsync<QueryResult<BaseItemDto>>(JsonDefaults.Options, TestContext.Current.CancellationToken);
            Assert.NotNull(views);
            Assert.DoesNotContain(views.Items, view => view.Type is BaseItemKind.Channel or BaseItemKind.LiveTvChannel);
            Assert.DoesNotContain(views.Items, view => string.Equals(view.CollectionType?.ToString(), "livetv", System.StringComparison.OrdinalIgnoreCase));
            var ids = views.Items.Select(view => view.Id.ToString()).ToArray();
            if (baseline is null)
            {
                baseline = ids;
            }
            else
            {
                Assert.Equal(baseline, ids);
            }
        }
    }
}
