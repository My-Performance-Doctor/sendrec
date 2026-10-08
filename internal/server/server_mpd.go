package server

import (
	"github.com/go-chi/chi/v5"
	"github.com/sendrec/sendrec/internal/mpd"
	"net/http"
)

func (s *Server) registerMPDMediaRoutes(cfg Config) {
	if cfg.Readiness != nil {
		s.router.Handle("/api/ready", cfg.Readiness)
	}
	if s.videoHandler == nil {
		return
	}
	s.videoHandler.BoundMPDMediaURLs()
	adapter := s.videoHandler.MPDAdapter(cfg.MPDService)
	s.router.Route("/api/integrations/mpd/videos/{videoId}", func(r chi.Router) {
		r.Use(adapter.Authenticate)
		r.Get("/", adapter.Metadata)
		r.Get("/transcript", adapter.Transcript)
		r.Put("/password", adapter.Password)
		r.Put("/publication", adapter.Publication)
	})
	media := s.videoHandler.MPDMedia(s.mpdHandler, cfg.MPDService, cfg.MPD.AllowedOrigins)
	s.router.Group(func(r chi.Router) {
		r.Use(s.authHandler.Middleware)
		r.Use(maxBodySize(64 * 1024))
		r.Get("/api/videos/{id}/mpd", media.Info)
		r.Post("/api/videos/{id}/preview", media.CreatePreview)
		r.Put("/api/videos/{id}/publication", media.Publish)
		r.Post("/api/videos/{id}/upload-url", s.videoHandler.RenewUpload)
	})
	s.router.Group(func(r chi.Router) {
		r.Use(s.watchLimiter.Middleware, maxBodySize(2048))
		r.Post("/api/mpd/preview/redeem", media.RedeemPreview)
		r.Post("/api/mpd/preview/media", media.PreviewMedia)
		r.Post("/api/mpd/preview/session", media.PreviewPlayback)
		r.Post("/api/mpd/preview/playback-start", media.PreviewPlayback)
	})
	s.router.Get("/mpd-preview", s.videoHandler.MPDPreviewPage)
	s.router.Get("/api/mpd/player.js", s.videoHandler.MPDPlayerScript)
	s.router.Group(func(r chi.Router) {
		r.Use(s.watchLimiter.Middleware, maxBodySize(2048), s.videoHandler.MPDPublicAccess)
		r.Get("/api/watch/{shareToken}/renew", s.videoHandler.RenewWatchMedia)
		r.With(s.videoHandler.MPDPlaybackAccess).Post("/api/watch/{shareToken}/playback-session", func(w http.ResponseWriter, r *http.Request) {
			var principal *mpd.Principal
			if c, err := r.Cookie("mpd_classification"); err == nil && s.mpdHandler != nil {
				validated, err := s.mpdHandler.ValidateClassification(r.Context(), c.Value)
				if err == nil {
					principal = validated
				}
			}
			s.videoHandler.MPDPlaybackSession(w, r, principal)
		})
		r.With(s.videoHandler.MPDPlaybackAccess).Post("/api/watch/{shareToken}/playback-start", s.videoHandler.MPDPlaybackStart)
	})
}
