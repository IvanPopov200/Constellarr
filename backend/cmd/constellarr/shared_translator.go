package main

import (
	"context"
	"fmt"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery/ai"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
)

// sharedTranslator runs subtitle translation through the stored discovery AI provider.
type sharedTranslator struct{ service *ai.Service }

func (t sharedTranslator) Translate(ctx context.Context, req subtitles.TranslationRequest) (subtitles.TranslationReply, error) {
	if t.service == nil {
		return subtitles.TranslationReply{}, unconfiguredError()
	}
	config, ok, err := t.service.Secret(ctx)
	if err != nil {
		return subtitles.TranslationReply{}, err
	}
	if !ok {
		return subtitles.TranslationReply{}, unconfiguredError()
	}
	// The client is built per request so a saved configuration applies to the next chunk.
	translator, err := subtitles.NewOpenAITranslator(subtitles.AIConfig{
		BaseURL: config.BaseURL, APIKey: config.APIKey, Model: config.Model,
	})
	if err != nil {
		return subtitles.TranslationReply{}, err
	}
	return translator.Translate(ctx, req)
}

// unconfiguredError keeps the subtitle service's sentinel and tells the user where to fix it.
func unconfiguredError() error {
	return fmt.Errorf("%w: set the base URL and model under Settings → Connections → AI provider", subtitles.ErrNotConfigured)
}
