// The token shapes the server routes on the short path (video.ShareTokenPattern in
// internal/video/video_query.go). Any other shape is served only on /watch/{token}.
const SHORT_SHARE_TOKEN = /^(?:[a-z0-9]{16}|[A-Za-z0-9_-]{22})$/;

/** A video's public path: the short one where the server routes it, else /watch/{token}. */
export function watchPath(shareToken: string): string {
  const token = encodeURIComponent(shareToken);
  return SHORT_SHARE_TOKEN.test(shareToken) ? `/${token}` : `/watch/${token}`;
}
