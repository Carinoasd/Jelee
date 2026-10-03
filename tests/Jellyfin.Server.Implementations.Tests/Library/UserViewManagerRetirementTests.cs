using System;
using System.Collections.Generic;
using Emby.Server.Implementations.Library;
using Jellyfin.Data.Enums;
using Jellyfin.Database.Implementations.Entities;
using Jellyfin.Database.Implementations.Enums;
using MediaBrowser.Controller.Channels;
using MediaBrowser.Controller.Configuration;
using MediaBrowser.Controller.Dto;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Library;
using MediaBrowser.Model.Configuration;
using MediaBrowser.Model.Globalization;
using MediaBrowser.Model.Library;
using MediaBrowser.Model.Querying;
using Moq;
using Xunit;

namespace Jellyfin.Server.Implementations.Tests.Library;

public sealed class UserViewManagerRetirementTests
{
    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public void GetUserViews_ExternalFlag_PreservesLocalFolder(bool external)
    {
        var user = new User("user", "auth", "reset");
        var folder = new Folder { Id = Guid.NewGuid(), Name = "Local library", ForcedSortName = "Local library" };
        var root = new Mock<Folder>();
        root.Setup(r => r.GetChildren(user, true, null)).Returns(new BaseItem[] { folder });
        var library = new Mock<ILibraryManager>(MockBehavior.Strict);
        library.Setup(l => l.GetUserRootFolder()).Returns(root.Object);
        library.Setup(l => l.Sort(It.IsAny<IEnumerable<BaseItem>>(), user, It.IsAny<IEnumerable<ItemSortBy>>(), SortOrder.Ascending))
            .Returns<IEnumerable<BaseItem>, User, IEnumerable<ItemSortBy>, SortOrder>((items, _, _, _) => items);
        var config = new Mock<IServerConfigurationManager>();
        config.SetupGet(c => c.Configuration).Returns(new ServerConfiguration { EnableFolderView = false });
        var manager = new UserViewManager(library.Object, Mock.Of<ILocalizationManager>(), config.Object);

        var views = manager.GetUserViews(new UserViewQuery { User = user, IncludeExternalContent = external });

        Assert.Same(folder, Assert.Single(views));
        library.Verify(l => l.GetUserRootFolder(), Times.Once());
    }

    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public void GetLatestItems_UsesLocalLibraryQueryForAnyPersistedFolder(bool retiredChannel)
    {
        var user = new User("user", "auth", "reset");
        Folder parent = retiredChannel ? new Channel() : new Folder();
        parent.Id = Guid.NewGuid();
        var movie = new Movie { Id = Guid.NewGuid(), Name = "Local movie" };
        var library = new Mock<ILibraryManager>(MockBehavior.Strict);
        library.Setup(l => l.GetItemById(parent.Id)).Returns(parent);
        library.Setup(l => l.GetItemList(It.IsAny<InternalItemsQuery>(), It.IsAny<List<BaseItem>>()))
            .Returns(new BaseItem[] { movie });
        var manager = new UserViewManager(library.Object, Mock.Of<ILocalizationManager>(), Mock.Of<IServerConfigurationManager>());

        var items = manager.GetLatestItems(
            new LatestItemsQuery { User = user, ParentId = parent.Id, IncludeItemTypes = Array.Empty<BaseItemKind>(), GroupItems = false, Limit = 3 },
            DtoOptions.StoredColumnsOnly);

        Assert.Same(movie, Assert.Single(Assert.Single(items).Item2));
        library.Verify(l => l.GetItemList(It.Is<InternalItemsQuery>(query => query.Limit == 6 && query.OrderBy[0].OrderBy == ItemSortBy.DateCreated), It.Is<List<BaseItem>>(parents => parents.Count == 1 && parents[0] == parent)), Times.Once());
    }
}
