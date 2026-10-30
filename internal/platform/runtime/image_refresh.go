package runtime

import (
	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

// prepareImageRefresh builds the remote artwork fetcher of image_refresh jobs
// (G40.9). It is nil, and refresh jobs cannot be submitted, unless image
// fetching is enabled and the processor keeps a persistent store: the
// default is the local-only image mode, in which no image request leaves the
// server.
//
// The controlled client allows any public HTTPS target (NFO artwork names
// arbitrary hosts); private, loopback and other non-public addresses stay
// refused unless developer mode relaxes SSRF checks (G45.4), like webhooks.
// The client carries no work budget: the verified body is stored under the
// processor's admission while the response is still open, and holding an I/O
// permit for the download while waiting for another would let a saturated
// budget wait on itself. The fetcher's own concurrency and per-host spacing
// bound the network side.
func prepareImageRefresh(c config.Config, processor *imageadapter.Processor, dev *devmode.Controller) (app.ItemImageFetcher, error) {
	if !c.EnableImageFetch || processor.OriginalStore() == nil {
		return nil, nil
	}
	client, err := outbound.New(nil)
	if err != nil {
		return nil, err
	}
	if dev != nil {
		client.AllowPrivateTargets(func() bool { return dev.Effective(devmode.RelaxSSRFStrict) })
	}
	fetcher, err := imageadapter.NewRemoteFetcher(client, imageadapter.FetchOptions{MaxBytes: processor.ItemImageLimit()})
	if err != nil {
		return nil, err
	}
	refresher, err := imageadapter.NewItemImageRefresher(fetcher, processor)
	if err != nil {
		return nil, err
	}
	return refresher, nil
}
