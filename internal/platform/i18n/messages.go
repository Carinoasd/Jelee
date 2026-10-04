// Package i18n provides the four supported server error-message locales.
package i18n

import "strings"

// Message translates a public error code. fallback must be a static, safe
// public message, never an underlying database or filesystem error string.
// This function performs no interpolation of request-controlled values.
func Message(code, acceptLanguage, fallback string) string {
	if message, ok := messages[Locale(acceptLanguage)][code]; ok {
		return message
	}
	return fallback
}

// The catalog is package-private and is never modified after initialization.
// Keep complete phrases together so their grammar is not assembled at runtime.
var messages = map[string]map[string]string{
	"en-US": {
		"image_busy":                "Image processing is busy. Try again later.",
		"image_unavailable":         "Image is unavailable.",
		"image_too_large":           "Image exceeds the processing limit.",
		"image_unsupported":         "Image format is not supported.",
		"metadata_unavailable":      "Metadata provider is unavailable. Try again later.",
		"feature_removed":           "Discovery, live TV, recordings and channels are not supported.",
		"nfo_disabled":              "NFO validation is disabled for this library.",
		"ignore_unavailable":        "Ignore scanning is unavailable.",
		"nfo_reader_unavailable":    "NFO validation is unavailable.",
		"nfo_cache_capacity":        "NFO cache capacity reached.",
		"nfo_identity_mismatch":     "NFO validation version changed. Retry the job.",
		"nfo_invalidated":           "NFO validation scope changed. Retry the job.",
		"probe_disabled":            "Media probing is disabled.",
		"probe_runtime_unavailable": "Media probing is unavailable. Check the isolated runtime.",
		"probe_cache_capacity":      "Probe cache capacity reached.",
		"probe_identity_mismatch":   "Probe tool identity changed. Retry the job.",
		"probe_invalidated":         "Probe scope changed. Retry the job.",
		"scan_unavailable":          "Scan root is unavailable.",
		"scan_limit":                "Scan resource limit reached.",
		"job_queue_full":            "Job queue capacity reached. Try again later.",
		"job_busy":                  "This library already has an active job.",
		"jobs_busy":                 "Job service is busy. Try again later.",
		"metrics_busy":              "Metrics service is busy. Try again later.",
		"account_busy":              "Account service is busy. Try again later.",
		"forbidden":                 "Operation is not permitted.",
		"conflict":                  "Resource conflicts with existing state.",
		"last_admin":                "An active administrator must remain.",
		"session_limit":             "Active session limit reached.",
		"native_login_disabled":     "Native device login is not enabled for this account.",
		"auth_rate_limited":         "Too many authentication attempts. Try again later.",
		"request_timeout":           "Request was cancelled or timed out.",
		"not_ready":                 "Service is not ready.",
		"playback_busy":             "Playback reporting is busy. Try again later.",
		"stats_export_limit":        "Export exceeds the row limit. Narrow the range.",
		"internal_error":            "Request could not be completed.",
		"invalid_host":              "Host is not allowed.",
		"not_found":                 "Resource was not found.",
		"authentication_required":   "Authentication is required.",
		"invalid_request":           "Request is invalid.",
		"web_playback_disabled":     "This session cannot play media.",
		"csrf_failed":               "Security token is missing or invalid. Reload the page and try again.",
		"transcode_disabled":        "Only original direct delivery is supported.",
		"stream_limit":              "Stream concurrency limit reached.",
		"user_stream_limit":         "Concurrent playback limit for this account reached.",
		"device_stream_limit":       "Concurrent playback limit for this device reached.",
		"lookup_timeout":            "Media lookup timed out.",
		"method_not_allowed":        "Method is not supported.",
		"invalid_range":             "Range cannot be satisfied.",
		"precondition_failed":       "Precondition failed.",
		"body_too_large":            "Request body exceeds the limit.",
		"unsupported_media_type":    "Request content type is not supported.",
	},
	"zh-CN": {
		"image_busy":                "图片处理繁忙，请稍后重试。",
		"image_unavailable":         "图片暂时不可用。",
		"image_too_large":           "图片超过处理上限。",
		"image_unsupported":         "不支持此图片格式。",
		"metadata_unavailable":      "元数据来源暂时不可用，请稍后重试。",
		"feature_removed":           "不支持设备发现、直播电视、录制和频道。",
		"nfo_disabled":              "此媒体库尚未启用NFO校验。",
		"ignore_unavailable":        "忽略规则扫描暂时不可用。",
		"nfo_reader_unavailable":    "NFO校验暂时不可用。",
		"nfo_cache_capacity":        "NFO缓存容量已达上限。",
		"nfo_identity_mismatch":     "NFO校验版本已改变，请重试任务。",
		"nfo_invalidated":           "NFO校验范围已改变，请重试任务。",
		"probe_disabled":            "媒体探测尚未启用。",
		"probe_runtime_unavailable": "媒体探测不可用，请检查隔离运行环境。",
		"probe_cache_capacity":      "探测缓存容量已达上限。",
		"probe_identity_mismatch":   "探测工具身份已改变，请重试任务。",
		"probe_invalidated":         "探测范围已改变，请重试任务。",
		"scan_unavailable":          "无法访问扫描根目录。",
		"scan_limit":                "扫描资源数量已达上限。",
		"job_queue_full":            "任务队列已满，请稍后重试。",
		"job_busy":                  "此媒体库已有正在等待或执行的任务。",
		"jobs_busy":                 "任务服务繁忙，请稍后重试。",
		"metrics_busy":              "指标服务繁忙，请稍后重试。",
		"account_busy":              "账户服务繁忙，请稍后重试。",
		"forbidden":                 "不允许执行此操作。",
		"conflict":                  "资源与现有状态冲突。",
		"last_admin":                "必须保留一名启用的管理员。",
		"session_limit":             "有效会话数量已达上限。",
		"native_login_disabled":     "此账户未启用原生设备登录。",
		"auth_rate_limited":         "身份验证尝试过于频繁，请稍后重试。",
		"request_timeout":           "请求已取消或超时。",
		"not_ready":                 "服务尚未就绪。",
		"playback_busy":             "播放进度上报繁忙，请稍后再试。",
		"stats_export_limit":        "导出超过行数上限，请缩小范围。",
		"internal_error":            "无法完成请求。",
		"invalid_host":              "不允许使用此主机名。",
		"not_found":                 "找不到该资源。",
		"authentication_required":   "请先登录。",
		"invalid_request":           "请求无效。",
		"web_playback_disabled":     "此会话不能播放媒体。",
		"csrf_failed":               "安全令牌缺失或无效，请重新加载页面后再试。",
		"transcode_disabled":        "仅支持原始文件直投。",
		"stream_limit":              "同时播放数量已达上限。",
		"user_stream_limit":         "此账户的同时播放数量已达上限。",
		"device_stream_limit":       "此设备的同时播放数量已达上限。",
		"lookup_timeout":            "媒体查询超时。",
		"method_not_allowed":        "不支持此请求方法。",
		"invalid_range":             "无法提供请求的字节范围。",
		"precondition_failed":       "请求的前置条件未满足。",
		"body_too_large":            "请求正文超出大小限制。",
		"unsupported_media_type":    "不支持此请求内容类型。",
	},
	"zh-TW": {
		"image_busy":                "圖片處理繁忙，請稍後重試。",
		"image_unavailable":         "圖片暫時無法使用。",
		"image_too_large":           "圖片超過處理上限。",
		"image_unsupported":         "不支援此圖片格式。",
		"metadata_unavailable":      "中繼資料來源暫時無法使用，請稍後重試。",
		"feature_removed":           "不支援裝置探索、直播電視、錄製與頻道。",
		"nfo_disabled":              "此媒體庫尚未啟用NFO驗證。",
		"ignore_unavailable":        "忽略規則掃描暫時無法使用。",
		"nfo_reader_unavailable":    "NFO驗證暫時無法使用。",
		"nfo_cache_capacity":        "NFO快取容量已達上限。",
		"nfo_identity_mismatch":     "NFO驗證版本已改變，請重試工作。",
		"nfo_invalidated":           "NFO驗證範圍已改變，請重試工作。",
		"probe_disabled":            "媒體探測尚未啟用。",
		"probe_runtime_unavailable": "媒體探測無法使用，請檢查隔離執行環境。",
		"probe_cache_capacity":      "探測快取容量已達上限。",
		"probe_identity_mismatch":   "探測工具身分已變更，請重試任務。",
		"probe_invalidated":         "探測範圍已變更，請重試任務。",
		"scan_unavailable":          "無法存取掃描根目錄。",
		"scan_limit":                "掃描資源數量已達上限。",
		"job_queue_full":            "任務佇列已滿，請稍後再試。",
		"job_busy":                  "此媒體庫已有正在等待或執行的任務。",
		"jobs_busy":                 "任務服務忙碌，請稍後再試。",
		"metrics_busy":              "指標服務忙碌，請稍後再試。",
		"account_busy":              "帳戶服務忙碌，請稍後再試。",
		"forbidden":                 "不允許執行此操作。",
		"conflict":                  "資源與現有狀態衝突。",
		"last_admin":                "必須保留一名啟用的管理員。",
		"session_limit":             "有效工作階段數量已達上限。",
		"native_login_disabled":     "此帳戶未啟用原生裝置登入。",
		"auth_rate_limited":         "身分驗證嘗試過於頻繁，請稍後再試。",
		"request_timeout":           "請求已取消或逾時。",
		"not_ready":                 "服務尚未就緒。",
		"playback_busy":             "播放進度回報忙碌中，請稍後再試。",
		"stats_export_limit":        "匯出超過列數上限，請縮小範圍。",
		"internal_error":            "無法完成請求。",
		"invalid_host":              "不允許使用此主機名稱。",
		"not_found":                 "找不到該資源。",
		"authentication_required":   "請先登入。",
		"invalid_request":           "請求無效。",
		"web_playback_disabled":     "此工作階段不能播放媒體。",
		"csrf_failed":               "安全權杖缺少或無效，請重新載入頁面後再試。",
		"transcode_disabled":        "僅支援原始檔案直投。",
		"stream_limit":              "同時播放數量已達上限。",
		"user_stream_limit":         "此帳號的同時播放數量已達上限。",
		"device_stream_limit":       "此裝置的同時播放數量已達上限。",
		"lookup_timeout":            "媒體查詢逾時。",
		"method_not_allowed":        "不支援此請求方法。",
		"invalid_range":             "無法提供請求的位元組範圍。",
		"precondition_failed":       "請求的前置條件未滿足。",
		"body_too_large":            "請求本文超出大小限制。",
		"unsupported_media_type":    "不支援此請求內容類型。",
	},
	"ja-JP": {
		"image_busy":                "画像処理が混み合っています。しばらくしてから再試行してください。",
		"image_unavailable":         "画像を利用できません。",
		"image_too_large":           "画像が処理上限を超えています。",
		"image_unsupported":         "この画像形式には対応していません。",
		"metadata_unavailable":      "メタデータの提供元を利用できません。後でもう一度お試しください。",
		"feature_removed":           "デバイス検出、ライブテレビ、録画、チャンネルには対応していません。",
		"nfo_disabled":              "このライブラリではNFO検証が無効です。",
		"ignore_unavailable":        "除外ルールを使ったスキャンを利用できません。",
		"nfo_reader_unavailable":    "NFO検証を利用できません。",
		"nfo_cache_capacity":        "NFOキャッシュの容量上限に達しました。",
		"nfo_identity_mismatch":     "NFO検証のバージョンが変更されました。ジョブを再試行してください。",
		"nfo_invalidated":           "NFO検証の範囲が変更されました。ジョブを再試行してください。",
		"probe_disabled":            "メディア解析は無効です。",
		"probe_runtime_unavailable": "メディア解析を利用できません。隔離実行環境を確認してください。",
		"probe_cache_capacity":      "解析キャッシュの容量が上限に達しました。",
		"probe_identity_mismatch":   "解析ツールが変更されました。タスクを再試行してください。",
		"probe_invalidated":         "解析対象が変更されました。タスクを再試行してください。",
		"scan_unavailable":          "スキャンのルートディレクトリにアクセスできません。",
		"scan_limit":                "スキャンのリソース数が上限に達しました。",
		"job_queue_full":            "ジョブキューが上限に達しました。しばらくしてから再試行してください。",
		"job_busy":                  "このライブラリには待機中または実行中のジョブがあります。",
		"jobs_busy":                 "ジョブサービスが混雑しています。しばらくしてから再試行してください。",
		"metrics_busy":              "メトリクスサービスが混雑しています。しばらくしてから再試行してください。",
		"account_busy":              "アカウントサービスが混雑しています。しばらくしてから再試行してください。",
		"forbidden":                 "この操作は許可されていません。",
		"conflict":                  "リソースが現在の状態と競合しています。",
		"last_admin":                "有効な管理者を少なくとも一人残す必要があります。",
		"session_limit":             "有効なセッション数が上限に達しました。",
		"native_login_disabled":     "このアカウントではネイティブデバイスからのログインが有効になっていません。",
		"auth_rate_limited":         "認証の試行が多すぎます。しばらくしてから再試行してください。",
		"request_timeout":           "リクエストはキャンセルされたか、タイムアウトしました。",
		"not_ready":                 "サービスの準備ができていません。",
		"playback_busy":             "再生状況の報告が混み合っています。しばらくしてからもう一度お試しください。",
		"stats_export_limit":        "エクスポートが行数の上限を超えています。範囲を狭めてください。",
		"internal_error":            "リクエストを完了できませんでした。",
		"invalid_host":              "このホスト名は許可されていません。",
		"not_found":                 "リソースが見つかりません。",
		"authentication_required":   "ログインしてください。",
		"invalid_request":           "リクエストが無効です。",
		"web_playback_disabled":     "このセッションではメディアを再生できません。",
		"csrf_failed":               "セキュリティトークンがないか無効です。ページを再読み込みしてもう一度お試しください。",
		"transcode_disabled":        "元のファイルの直接配信のみ対応しています。",
		"stream_limit":              "同時再生数の上限に達しました。",
		"user_stream_limit":         "このアカウントの同時再生数の上限に達しました。",
		"device_stream_limit":       "このデバイスの同時再生数の上限に達しました。",
		"lookup_timeout":            "メディアの検索がタイムアウトしました。",
		"method_not_allowed":        "このリクエストメソッドには対応していません。",
		"invalid_range":             "指定されたバイト範囲を提供できません。",
		"precondition_failed":       "リクエストの前提条件が満たされていません。",
		"body_too_large":            "リクエスト本文がサイズの上限を超えています。",
		"unsupported_media_type":    "このリクエストのコンテンツタイプには対応していません。",
	},
}

type preference struct {
	tag     string
	quality int
	order   int
}

// Locale negotiates a supported locale. No header selects zh-CN; an unknown,
// malformed, or wholly excluded selection falls back silently to en-US.
// Applications with a persisted user preference should pass that locale ahead
// of the header; this package never trusts an unsigned user preference field.
func Locale(header string) string {
	if strings.TrimSpace(header) == "" {
		return "zh-CN"
	}
	if len(header) > 8192 {
		return "en-US"
	}
	preferences := parsePreferences(header)
	bestLocale, bestQuality, bestOrder := "en-US", -1, len(preferences)+8192
	for _, locale := range []string{"zh-CN", "zh-TW", "ja-JP", "en-US"} {
		quality, order, specificity := -1, 8192, -1
		for _, preference := range preferences {
			match := languageMatch(preference.tag, locale)
			if match < 0 {
				continue
			}
			if match > specificity || (match == specificity && preference.quality > quality) {
				quality, order, specificity = preference.quality, preference.order, match
			}
		}
		if quality > 0 && (quality > bestQuality || (quality == bestQuality && order < bestOrder)) {
			bestLocale, bestQuality, bestOrder = locale, quality, order
		}
	}
	return bestLocale
}

func parsePreferences(header string) []preference {
	var result []preference
	for order, entry := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ";")
		tag := strings.ToLower(strings.TrimSpace(parts[0]))
		if !validTag(tag) || len(parts) > 2 {
			continue
		}
		quality := 1000
		if len(parts) == 2 {
			key, value, ok := strings.Cut(strings.TrimSpace(parts[1]), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			quality = parseQuality(strings.TrimSpace(value))
			if quality < 0 {
				continue
			}
		}
		result = append(result, preference{tag: tag, quality: quality, order: order})
	}
	return result
}

func validTag(tag string) bool {
	if tag == "*" {
		return true
	}
	parts := strings.Split(tag, "-")
	for i, part := range parts {
		if len(part) < 1 || len(part) > 8 {
			return false
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (i == 0 || char < '0' || char > '9') {
				return false
			}
		}
	}
	return true
}

func parseQuality(value string) int {
	whole, fraction, decimal := strings.Cut(value, ".")
	if whole != "0" && whole != "1" {
		return -1
	}
	if decimal && len(fraction) > 3 {
		return -1
	}
	quality := 0
	if whole == "1" {
		quality = 1000
	}
	weight := 100
	for _, digit := range fraction {
		if digit < '0' || digit > '9' || (whole == "1" && digit != '0') {
			return -1
		}
		quality += int(digit-'0') * weight
		weight /= 10
	}
	return quality
}

func languageMatch(tag, locale string) int {
	if tag == "*" {
		return 0
	}
	if tag == strings.ToLower(locale) {
		return 3
	}
	parts := strings.Split(tag, "-")
	switch parts[0] {
	case "en":
		if locale == "en-US" {
			return 1
		}
	case "ja":
		if locale == "ja-JP" {
			return 1
		}
	case "zh":
		if !strings.HasPrefix(locale, "zh-") {
			return -1
		}
		variant := ""
		for _, part := range parts[1:] {
			switch part {
			case "hant", "tw", "hk", "mo":
				variant = "zh-TW"
			case "hans", "cn", "sg":
				variant = "zh-CN"
			}
		}
		if variant == "" {
			return 1
		}
		if locale == variant {
			return 2
		}
	}
	return -1
}
