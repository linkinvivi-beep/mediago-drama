package generation

const (
	FamilyCodexImage = "codex-image"
	FamilyZImage     = "z-image"
	FamilyMiniMaxH3  = "minimax-h3"

	VersionCodexImageV1 = "codex-image-v1"
	VersionZImageV1     = "z-image-v1"
	VersionMiniMaxH3V1  = "minimax-h3-v1"
)

var mediaLinkFamilySpecs = []familySpec{
	{
		Family: ModelFamily{
			ID:          FamilyCodexImage,
			Label:       "Codex Image",
			Kind:        KindImage,
			Description: "Image generation through the bundled Codex session",
		},
		Versions: []ModelVersion{
			version(VersionCodexImageV1, FamilyCodexImage, "Codex Image", KindImage, "imagegen", false, true),
		},
		Routes: []ModelRoute{
			mediaLinkRoute(
				RouteCodexImage,
				FamilyCodexImage,
				VersionCodexImageV1,
				"Codex 生图",
				KindImage,
				ProviderCodex,
				"imagegen",
				AdapterCodexImage,
				"https://developers.openai.com/codex/app-server",
				codexImageParams(),
				false,
			),
		},
	},
	{
		Family: ModelFamily{
			ID:          FamilyZImage,
			Label:       "Z-Image",
			Kind:        KindImage,
			Description: "Z-Image generation through AutoDL ComfyUI",
		},
		Versions: []ModelVersion{
			version(VersionZImageV1, FamilyZImage, "Z-Image", KindImage, "z-image", false, true),
		},
		Routes: []ModelRoute{
			mediaLinkRoute(
				RouteAutoDLImage,
				FamilyZImage,
				VersionZImageV1,
				"AutoDL · 云端生图",
				KindImage,
				ProviderAutoDL,
				"z-image",
				AdapterAutoDLComfyImage,
				"https://docs.comfy.org/development/core-concepts/api",
				autoDLImageParams(),
				false,
				withReferenceURLLimit(1),
			),
		},
	},
	{
		Family: ModelFamily{
			ID:          FamilyMiniMaxH3,
			Label:       "MiniMax H3",
			Kind:        KindVideo,
			Description: "MiniMax H3 video generation through AutoDL ComfyUI",
		},
		Versions: []ModelVersion{
			version(VersionMiniMaxH3V1, FamilyMiniMaxH3, "MiniMax H3", KindVideo, "minimax-h3", true, true),
		},
		Routes: []ModelRoute{
			mediaLinkRoute(
				RouteAutoDLH3,
				FamilyMiniMaxH3,
				VersionMiniMaxH3V1,
				"AutoDL · MiniMax H3",
				KindVideo,
				ProviderAutoDL,
				"minimax-h3",
				AdapterAutoDLComfyH3Video,
				"https://docs.comfy.org/development/core-concepts/api",
				autoDLH3Params(),
				true,
			),
		},
	},
}

func mediaLinkRoute(
	id string,
	familyID string,
	versionID string,
	label string,
	kind Kind,
	provider string,
	model string,
	adapter string,
	docURL string,
	params RouteParamConfig,
	async bool,
	options ...routeOption,
) ModelRoute {
	route := ModelRoute{
		ID:                    id,
		FamilyID:              familyID,
		VersionID:             versionID,
		Label:                 label,
		Kind:                  kind,
		Provider:              provider,
		Model:                 model,
		Adapter:               adapter,
		DocURL:                docURL,
		Async:                 async,
		SupportsReferenceURLs: true,
		Status:                RouteStatusAvailable,
		Params:                routeParamSpecs(kind, params.CanonicalParams),
		ParamGroups:           routeParamGroups(kind, params.CanonicalParams),
		Combos:                cloneParamCombos(params.Combos),
		CanonicalParams:       params.CanonicalParams,
		Translation:           params.Translation,
	}
	applyRouteOptions(&route, options...)
	return route
}

func codexImageParams() RouteParamConfig {
	return identityRouteParamConfig([]RouteParam{
		selectRouteParam(ParamAspectRatio, "1:1", []ParamOption{
			{Label: "1:1", Value: "1:1"},
			{Label: "3:2", Value: "3:2"},
			{Label: "2:3", Value: "2:3"},
			{Label: "16:9", Value: "16:9"},
			{Label: "9:16", Value: "9:16"},
		}),
	})
}

func autoDLImageParams() RouteParamConfig {
	return identityRouteParamConfig([]RouteParam{
		selectRouteParam(ParamAspectRatio, "1:1", []ParamOption{
			{Label: "1:1", Value: "1:1"},
			{Label: "3:2", Value: "3:2"},
			{Label: "2:3", Value: "2:3"},
			{Label: "16:9", Value: "16:9"},
			{Label: "9:16", Value: "9:16"},
		}),
		selectRouteParam(ParamResolution, "1K", []ParamOption{
			{Label: "512px", Value: "512px"},
			{Label: "1K", Value: "1K"},
			{Label: "2K", Value: "2K"},
		}),
		optionalNumberRouteParam(ParamSeed, -1, 2147483647),
	})
}

func autoDLH3Params() RouteParamConfig {
	return identityRouteParamConfig([]RouteParam{
		selectRouteParam(ParamDuration, "4", mediaLinkH3DurationOptions()),
		selectRouteParam(ParamAspectRatio, "16:9", []ParamOption{
			{Label: "16:9", Value: "16:9"},
			{Label: "9:16", Value: "9:16"},
			{Label: "1:1", Value: "1:1"},
		}),
		selectRouteParam(ParamResolution, "1080p", []ParamOption{
			{Label: "720p", Value: "720p"},
			{Label: "1080p", Value: "1080p"},
		}),
		optionalNumberRouteParam(ParamSeed, -1, 2147483647),
		selectRouteParam(ParamProfileKind, "ref2va", []ParamOption{
			{Label: "REF2VA", Value: "ref2va"},
			{Label: "FL2VA", Value: "fl2va"},
		}),
	})
}

func mediaLinkH3DurationOptions() []ParamOption {
	options := make([]ParamOption, 0, 12)
	for duration := 4; duration <= 15; duration++ {
		value := formatNumber(float64(duration))
		options = append(options, ParamOption{Label: value + "s", Value: value})
	}
	return options
}
