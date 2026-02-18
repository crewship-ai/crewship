import type { NextConfig } from "next"

const isDev = process.env.NODE_ENV === "development"

const nextConfig: NextConfig = {
  ...(isDev ? {} : { output: "export" }),
  images: {
    unoptimized: true,
  },
  async rewrites() {
    if (!isDev) return []
    return [
      {
        source: "/api/:path*",
        destination: "http://localhost:8080/api/:path*",
      },
    ]
  },
}

export default nextConfig
