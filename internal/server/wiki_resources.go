package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/pii"
	"github.com/wuxujun/xktmcp/internal/trace"
	wikibackend "github.com/wuxujun/xktmcp/internal/wiki"
)

type wikiResourceBackend interface {
	ListResources(context.Context, string, int) (wikibackend.ResourceCatalog, error)
	ReadPageResource(context.Context, string, string) (string, error)
	ListTree(context.Context, string, string, int) ([]model.WikiNode, error)
}

func registerWikiResources(s *mcp.Server, backend wikiResourceBackend, cfg wikibackend.ResourceConfig) {
	s.AddResource(&mcp.Resource{
		URI:      "wiki://catalog",
		Name:     "wiki-catalog",
		Title:    "Wiki 资源目录",
		MIMEType: "application/json",
	}, wikiCatalogHandler(backend, cfg.MaxCatalogEntries, cfg.LinkBaseURL))
	s.AddResource(&mcp.Resource{
		URI:      "wiki://tree",
		Name:     "wiki-tree",
		Title:    "Wiki 目录树",
		MIMEType: "application/json",
	}, wikiTreeHandler(backend))
	pageHandler := wikiPageHandler(backend, cfg.LinkBaseURL)
	legacyName := "wiki-page"
	if cfg.LinkBaseURL != "" {
		s.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: cfg.LinkBaseURL + "/{page_key}",
			Name:        "wiki-page",
			Title:       "Wiki 页面",
			MIMEType:    "text/markdown",
		}, pageHandler)
		legacyName = "wiki-page-legacy"
	}
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "wiki://page/{page_key}",
		Name:        legacyName,
		Title:       "Wiki 页面",
		MIMEType:    "text/markdown",
	}, pageHandler)
}

func wikiCatalogHandler(backend wikiResourceBackend, maxCatalogEntries int, linkBaseURL string) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		const uri = "wiki://catalog"
		if req == nil || req.Params == nil || req.Params.URI != uri {
			return nil, mcp.ResourceNotFoundError(requestedResourceURI(req))
		}
		userID := trace.EffectiveUserID(ctx, "")
		catalog, err := backend.ListResources(ctx, userID, maxCatalogEntries)
		if err != nil {
			return nil, wikiResourceError(ctx, uri, err)
		}
		for i := range catalog.Items {
			pageID, err := wikibackend.ParsePageResourceURI(catalog.Items[i].URI)
			if err != nil {
				return nil, wikiResourceError(ctx, uri, err)
			}
			catalog.Items[i].URI, err = wikibackend.PageResourceLinkURI(pageID, linkBaseURL)
			if err != nil {
				return nil, wikiResourceError(ctx, uri, err)
			}
		}
		raw, err := json.Marshal(catalog)
		if err != nil {
			return nil, wikiResourceError(ctx, uri, err)
		}
		return wikiResourceResult(uri, "application/json", string(raw)), nil
	}
}

func wikiTreeHandler(backend wikiResourceBackend) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		const uri = "wiki://tree"
		if req == nil || req.Params == nil || req.Params.URI != uri {
			return nil, mcp.ResourceNotFoundError(requestedResourceURI(req))
		}
		userID := trace.EffectiveUserID(ctx, "")
		nodes, err := backend.ListTree(ctx, userID, "", 10)
		if err != nil {
			return nil, wikiResourceError(ctx, uri, err)
		}
		text, _ := pii.RedactJSON(nodes)
		return wikiResourceResult(uri, "application/json", text), nil
	}
}

func wikiPageHandler(backend wikiResourceBackend, linkBaseURL string) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if req == nil || req.Params == nil {
			return nil, mcp.ResourceNotFoundError(requestedResourceURI(req))
		}
		uri := req.Params.URI
		pageID, err := wikibackend.ParsePageResourceLinkURI(uri, linkBaseURL)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		backendURI, err := wikibackend.PageResourceURI(pageID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		userID := trace.EffectiveUserID(ctx, "")
		text, err := backend.ReadPageResource(ctx, userID, backendURI)
		if err != nil {
			return nil, wikiResourceError(ctx, uri, err)
		}
		return wikiResourceResult(uri, "text/markdown", text), nil
	}
}

func wikiResourceResult(uri, mimeType, text string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: mimeType,
		Text:     text,
	}}}
}

func requestedResourceURI(req *mcp.ReadResourceRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	return req.Params.URI
}

func wikiResourceError(ctx context.Context, uri string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, wikibackend.ErrResourceNotFound) || errors.Is(err, wikibackend.ErrUserWikiNotConfigured) {
		return mcp.ResourceNotFoundError(uri)
	}
	logger.ErrorfCtx(ctx, "read wiki resource failed: uri=%s error=%v", pii.MaskSubject(uri), err)
	return errors.New("read wiki resource failed")
}
