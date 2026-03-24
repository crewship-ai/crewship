import type { NextConfig } from "next"

const isDev = process.env.NODE_ENV === "development"
const goPort = process.env.NEXT_PUBLIC_GO_PORT || "8080"

const nextConfig: NextConfig = {
  ...(isDev ? {} : { output: "export" }),
  allowedDevOrigins: ["192.168.1.201"],
  images: {
    unoptimized: true,
  },
  async rewrites() {
    if (!isDev) return []
    return [
      {
        source: "/api/:path*",
        destination: `http://localhost:${goPort}/api/:path*`,
      },
    ]
  },
}

export default nextConfig
