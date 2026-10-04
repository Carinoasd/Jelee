package compat

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Image module (G24.2, G40.8, G48.3). The item image routes hand a parsed
// request to the server's own image pipeline (the one behind /images): the
// layer never decodes, resizes or caches a picture itself, and the
// pipeline's limits, admission, deadline, validators and cache policy apply
// unchanged. Listings advertise a stable tag for every image slot the item
// has, read for a whole page in one batched lookup.
//
// The width and height members of these routes resize a picture, which the
// image pipeline is built for (G40.6); they are not a request to convert
// media. router.boundary therefore inspects the image routes with
// media.GuardImage, which reads only the documented image members as such
// and still answers every video transformation parameter with 409. Every
// stream route keeps media.GuardProduction, where width or maxWidth remain
// transformation requests.

// Images is the server's image pipeline.
type Images interface {
	// ServeImage renders one item image for actor and writes the response.
	// It writes nothing when it returns an error: the caller answers it in
	// the layer's error format. Missing and invisible items are both
	// domain.ErrNotFound.
	ServeImage(w http.ResponseWriter, r *http.Request, actor domain.Actor, image ImageDelivery) error
	// ImageSummaries returns the selected source of every image slot of the
	// items among itemIDs that userID can see, in one read.
	ImageSummaries(ctx context.Context, userID string, itemIDs []string) (map[string][]domain.ItemImageSummary, error)
}

// ImageDelivery is one parsed image request.
type ImageDelivery struct {
	ItemID  string
	Request domain.ImageRequest
	// Tag is the client's cache tag when it has the form of one, else
	// empty. A tag naming the original's content digest selects the
	// immutable cache policy; any other value is only a cache key.
	Tag string
	// Vary names the request headers that carry the caller's credentials.
	Vary string
}

// imageVary lists every header the layer reads a credential from, so a
// private cache never serves one caller's copy to another. A token in the
// query is part of the URL already.
var imageVary = strings.Join([]string{"Authorization", headerLegacyAuthorization, headerLegacyToken, headerLegacyTokenAlt}, ", ")

// imageKind is one upstream ImageType and the Jelee slots that serve it, in
// the order they are tried. Upstream stores clear logos, clear art and
// landscape thumbs under Logo, Art and Thumb; Jelee keeps them apart (G40.1)
// and falls back to them. An empty slot list (Screenshot) has no Jelee
// equivalent.
type imageKind struct {
	name  string
	slots []string
}

// imageKinds are the upstream ImageType names in their enum order.
var imageKinds = []imageKind{
	{imageTypePrimary, []string{"Primary"}},
	{imageTypeArt, []string{"Art", "ClearArt"}},
	{imageTypeBackdrop, []string{"Backdrop"}},
	{imageTypeBanner, []string{"Banner"}},
	{imageTypeLogo, []string{"Logo", "ClearLogo"}},
	{imageTypeThumb, []string{"Thumb", "Landscape"}},
	{imageTypeDisc, []string{"Disc"}},
	{imageTypeBox, []string{"Box"}},
	{imageTypeScreenshot, nil},
	{imageTypeMenu, []string{"Menu"}},
	{imageTypeChapter, []string{"Chapter"}},
	{imageTypeBoxRear, []string{"BoxRear"}},
	{imageTypeProfile, []string{"Profile"}},
}

// lookupImageKind matches an upstream ImageType name case-insensitively, as
// upstream binds enum route values.
func lookupImageKind(name string) (imageKind, bool) {
	for _, kind := range imageKinds {
		if strings.EqualFold(kind.name, name) {
			return kind, true
		}
	}
	return imageKind{}, false
}

// Image size ceiling of a request. Larger values are lowered to it rather
// than refused (clients ask for screen-sized backdrops); the pipeline then
// applies its own configured output limit.
const imageDimensionMax = 2048

func (rt *router) imageRoutes() {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rt.handle(method, "/Items/{itemId}/Images/{imageType}", true, rt.itemImage)
		rt.handle(method, "/Items/{itemId}/Images/{imageType}/{imageIndex}", true, rt.itemImage)
	}
}

// isImagePath reports whether the path below Prefix names an item image
// route, compared like routing (letter case, one trailing slash).
func isImagePath(rest string) bool {
	rest = strings.TrimSuffix(rest, "/")
	segments := strings.Split(rest, "/")
	if len(segments) != 5 && len(segments) != 6 || segments[0] != "" || !strings.EqualFold(segments[1], "Items") || !strings.EqualFold(segments[3], "Images") {
		return false
	}
	for _, segment := range segments[1:] {
		if segment == "" {
			return false
		}
	}
	return true
}

// itemImage serves GET and HEAD of one item image.
//
// Upstream lets anyone fetch an item image without credentials. Here the
// route needs a native session like every other library route (a token in
// api_key or ApiKey is accepted, which is how players put credentials into
// image URLs): knowing an item identifier must not be enough to see its
// artwork (G48.3), and the image is read with the caller's own library
// grant, so a missing and an invisible item get the same answer.
func (rt *router) itemImage(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		writeError(w, http.StatusUnauthorized)
		return
	}
	itemID, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	kind, ok := lookupImageKind(chi.URLParam(r, "imageType"))
	if !ok {
		writeError(w, http.StatusBadRequest)
		return
	}
	q := readQuery(r)
	index := 0
	if raw := chi.URLParam(r, "imageIndex"); raw != "" {
		if index, err = imageIndex(raw); err != nil {
			writeError(w, http.StatusBadRequest)
			return
		}
	} else if raw := q.get("imageindex"); raw != "" {
		if index, err = imageIndex(raw); err != nil {
			writeError(w, http.StatusBadRequest)
			return
		}
	}
	delivery, err := parseImageQuery(q)
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	delivery.ItemID, delivery.Vary = itemID, imageVary
	actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: rt.opts.Library.ClientIP(r)}
	// A slot that cannot exist (Screenshot, an index on a single image
	// type) is answered like an item without that image, whoever asks.
	err = domain.ErrNotFound
	for _, slot := range kind.slots {
		if !domain.ValidItemImageSlot(slot, index) {
			continue
		}
		delivery.Request.Type, delivery.Request.Index = slot, index
		err = rt.opts.Library.Images.ServeImage(w, r, actor, delivery)
		if err == nil {
			return
		}
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrImageUnavailable) {
			break
		}
	}
	rt.writeImageError(w, err)
}

func imageIndex(raw string) (int, error) {
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || v < 0 {
		return 0, errBadQuery
	}
	if v > domain.ItemImageMaxIndex {
		// No slot holds it; answered as a missing image.
		return domain.ItemImageMaxIndex + 1, nil
	}
	return int(v), nil
}

// parseImageQuery reads the documented members of the upstream item image
// routes, case-insensitively. Width, MaxWidth and FillWidth (and the height
// members) all bound the output: the pipeline keeps the aspect ratio, never
// enlarges and never crops, so the smallest bound given wins and a fill box
// is not covered beyond the original. Quality 0 means the default. Format
// must be an upstream ImageFormat name, but the output stays JPEG, which
// the Content-Type states. The overlay and effect members (PercentPlayed,
// UnplayedCount, Blur, BackgroundColor, ForegroundLayer) are checked for
// syntax and ignored: the picture is delivered without them. Other members
// are ignored like upstream; transformation parameters never reach here
// (media.GuardImage).
func parseImageQuery(q browseQuery) (ImageDelivery, error) {
	var d ImageDelivery
	bound := func(names ...string) (int, error) {
		result := 0
		for _, name := range names {
			v, set, err := q.integer(name)
			if err != nil {
				return 0, err
			}
			if set && v > 0 && (result == 0 || v < result) {
				result = v
			}
		}
		return min(result, imageDimensionMax), nil
	}
	var err error
	if d.Request.Width, err = bound("width", "maxwidth", "fillwidth"); err != nil {
		return d, err
	}
	if d.Request.Height, err = bound("height", "maxheight", "fillheight"); err != nil {
		return d, err
	}
	quality, _, err := q.integer("quality")
	if err != nil {
		return d, err
	}
	d.Request.Quality = min(quality, 100)
	if raw := q.get("format"); raw != "" && !upstreamImageFormat(raw) {
		return d, errBadQuery
	}
	d.Request.Format = "jpeg"
	for _, name := range []string{"unplayedcount", "blur"} {
		if _, _, err := q.integer(name); err != nil {
			return d, err
		}
	}
	if raw := q.get("percentplayed"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return d, errBadQuery
		}
	}
	if tag := q.get("tag"); validImageTag(tag) {
		d.Tag = tag
	}
	return d, nil
}

// upstreamImageFormat reports whether name is an upstream ImageFormat.
func upstreamImageFormat(name string) bool {
	for _, format := range []string{"Bmp", "Gif", "Jpg", "Png", "Webp", "Svg"} {
		if strings.EqualFold(name, format) {
			return true
		}
	}
	return false
}

// validImageTag accepts the tag forms the pipeline can compare: 1–128
// hexadecimal digits. Anything else is not a tag this server issued.
func validImageTag(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// writeImageError answers a failed image request. A missing or invisible
// item, and an item without that image, get the hidden status; a picture
// the pipeline cannot use is 404 (the item was visible to reach it).
func (rt *router) writeImageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		writeError(w, rt.opts.Library.HiddenStatus)
	case errors.Is(err, domain.ErrImageUnavailable):
		writeError(w, http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, domain.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized)
	case errors.Is(err, domain.ErrImageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge)
	case errors.Is(err, domain.ErrImageUnsupported):
		writeError(w, http.StatusUnsupportedMediaType)
	case errors.Is(err, domain.ErrImageBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable)
	case errors.Is(err, domain.ErrDatabase), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable)
	default:
		writeError(w, http.StatusInternalServerError)
	}
}

// imageOptions are the listing members that choose which image tags an
// item carries (upstream DtoOptions: EnableImages, ImageTypeLimit,
// EnableImageTypes).
type imageOptions struct {
	enabled bool
	// limit is the number of images per type; -1 is no limit.
	limit int
	// types restricts the types when non-nil (lower-case upstream names).
	types map[string]bool
}

func defaultImageOptions() imageOptions { return imageOptions{enabled: true, limit: -1} }

func parseImageOptions(q browseQuery) (imageOptions, error) {
	opts := defaultImageOptions()
	enabled, set, err := q.boolean("enableimages")
	if err != nil {
		return opts, err
	}
	if set {
		opts.enabled = enabled
	}
	limit, set, err := q.integer("imagetypelimit")
	if err != nil {
		return opts, err
	}
	if set {
		opts.limit = limit
	}
	if names := q.list("enableimagetypes"); len(names) > 0 {
		opts.types = map[string]bool{}
		for _, name := range names {
			if !identifier(name) {
				return opts, errBadQuery
			}
			opts.types[strings.ToLower(name)] = true
		}
	}
	return opts, nil
}

// typeLimit is the upstream GetImageLimit: how many images of one type an
// item may list.
func (o imageOptions) typeLimit(name string) int {
	if o.types != nil && !o.types[strings.ToLower(name)] {
		return 0
	}
	if o.limit < 0 {
		return math.MaxInt
	}
	return o.limit
}

// attachImages fills ImageTags, BackdropImageTags and, when asked for,
// PrimaryImageAspectRatio of the items among dtos (native identifiers ids)
// from one batched read for userID. Library folders have no images. Without
// the image module every item keeps empty tags.
func (rt *router) attachImages(ctx context.Context, userID string, dtos []baseItemDto, ids []string, opts imageOptions, aspect bool) error {
	backdrops := min(opts.typeLimit(imageTypeBackdrop), domain.ItemImageSummaryGalleryMax)
	for i := range dtos {
		if dtos[i].Type == itemTypeCollectionFolder {
			continue
		}
		if !opts.enabled {
			dtos[i].ImageTags = nil
		}
		if backdrops == 0 {
			dtos[i].BackdropImageTags = nil
		}
	}
	images := rt.opts.Library.Images
	if images == nil || !opts.enabled && backdrops == 0 && !aspect {
		return nil
	}
	var wanted []string
	for i := range dtos {
		if dtos[i].Type != itemTypeCollectionFolder {
			wanted = append(wanted, ids[i])
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	summaries, err := images.ImageSummaries(ctx, userID, wanted)
	if err != nil {
		return err
	}
	for i := range dtos {
		found := summaries[ids[i]]
		if len(found) == 0 || dtos[i].Type == itemTypeCollectionFolder {
			continue
		}
		slots := make(map[string]map[int]domain.ItemImageSummary, len(found))
		for _, s := range found {
			if slots[s.Type] == nil {
				slots[s.Type] = map[int]domain.ItemImageSummary{}
			}
			slots[s.Type][s.Index] = s
		}
		if opts.enabled {
			for _, kind := range imageKinds {
				if kind.name == imageTypeBackdrop || kind.name == imageTypeChapter || opts.typeLimit(kind.name) == 0 {
					continue
				}
				for _, slot := range kind.slots {
					if s, ok := slots[slot][0]; ok {
						dtos[i].ImageTags[kind.name] = s.Tag()
						break
					}
				}
			}
		}
		// Clients address backdrops by position, so only the gapless run
		// from index 0 is listed; position and Jelee index then agree.
		for index := 0; index < backdrops; index++ {
			s, ok := slots["Backdrop"][index]
			if !ok {
				break
			}
			dtos[i].BackdropImageTags = append(dtos[i].BackdropImageTags, s.Tag())
		}
		if s, ok := slots["Primary"][0]; aspect && ok && s.Width > 0 && s.Height > 0 {
			ratio := float64(s.Width) / float64(s.Height)
			dtos[i].PrimaryImageAspectRatio = &ratio
		}
	}
	return nil
}
