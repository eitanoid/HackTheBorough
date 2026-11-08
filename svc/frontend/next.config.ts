import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  cacheComponents: true,
  partialPrefetching: true,
  experimental: {
    proxyClientMaxBodySize: "30mb",
  },
  async rewrites() {
    const backendURL = (process.env.BACKEND_API_URL ?? "http://127.0.0.1:8081").replace(/\/$/, "");
    return [
      {
        source: "/rpc/:path*",
        destination: `${backendURL}/:path*`,
      },
      { source: "/api/sources", destination: `${backendURL}/api/sources` },
      { source: "/api/sources/:path*", destination: `${backendURL}/api/sources/:path*` },
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
