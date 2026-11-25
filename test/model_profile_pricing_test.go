package test

import (
	"context"
	"testing"

	maibotcfg "MaiGo/internal/config/maibot"
	modelprofiles "MaiGo/internal/config/model_profiles"
	"MaiGo/internal/llm_models"
)

func TestModelProfileUsesAlignedModelPrices(t *testing.T) {
	file := maibotcfg.ModelConfigFile{
		Models: []maibotcfg.ModelInfo{{
			Name:            "profile-alias",
			ModelIdentifier: "provider-model",
			ApiProvider:     "test-provider",
			ModelType:       "llm",
			PriceIn:         1.5,
			CachePriceIn:    0.2,
			PriceOut:        3,
			PricePeriods: []maibotcfg.ModelPricePeriod{{
				StartTime: "00:00", EndTime: "12:00", PriceIn: 2, CachePriceIn: 0.3, PriceOut: 4,
			}},
		}},
		ApiProviders: []maibotcfg.APIProvider{{
			Name:       "test-provider",
			BaseUrl:    "http://127.0.0.1:8080",
			ApiKey:     "test-key",
			ClientType: "openai",
			AuthType:   "bearer",
		}},
	}
	profiles, err := modelprofiles.NewStaticStore(
		"profile-alias", "openai", "http://127.0.0.1:8080", "provider-model", "test-key",
	)
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(profiles, &file)
	router.BindProfile(llm.TaskReplyer, "profile-alias")
	route, err := router.Resolve(context.Background(), llm.TaskReplyer)
	if err != nil {
		t.Fatal(err)
	}
	if route.Config.PriceIn != 1.5 || route.Config.CachePriceIn != 0.2 || route.Config.PriceOut != 3 {
		t.Fatalf("profile prices = in %v / cache %v / out %v", route.Config.PriceIn, route.Config.CachePriceIn, route.Config.PriceOut)
	}
	if len(route.Config.PricePeriods) != 1 || route.Config.PricePeriods[0].PriceIn != 2 {
		t.Fatalf("profile price periods = %+v", route.Config.PricePeriods)
	}
}
