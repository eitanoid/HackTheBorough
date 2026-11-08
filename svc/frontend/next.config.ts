import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /* config options here */
  cacheComponents: true,
  partialPrefetching: true,
  async rewrites() {
    const backendURL = (process.env.BACKEND_API_URL ?? "http://127.0.0.1:8081").replace(/\/$/, "");
    return [
      {
        source: "/rpc/:path*",
        destination: `${backendURL}/:path*`,
      },
    ];
  },
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
