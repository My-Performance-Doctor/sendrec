import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/watch": "http://localhost:8080",
      // Short share links, in the shapes internal/server/server.go routes to the watch page.
      "^/(?:[a-z0-9]{16}|[A-Za-z0-9_-]{22})(?:\\?|$)": "http://localhost:8080",
      // The branding preview is a server-rendered page the settings iframe
      // loads; without this the dev server answers it with the SPA shell.
      "/branding": "http://localhost:8080",
    },
  },
});
