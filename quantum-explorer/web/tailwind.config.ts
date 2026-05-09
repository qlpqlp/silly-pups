import type { Config } from "tailwindcss";

export default {
  darkMode: "class",
  content: ["./src/**/*.{js,ts,jsx,tsx,mdx}"],
  theme: {
    extend: {
      fontFamily: {
        sans: ["var(--font-sans)", "ui-sans-serif", "system-ui"],
        comic: ["var(--font-sans)", "ui-sans-serif", "system-ui"],
      },
      colors: {
        doge: {
          gold: "#f2c94c",
          deep: "#c99400",
          ink: "#1c1410",
        },
      },
      boxShadow: {
        glass: "0 4px 24px rgba(0, 0, 0, 0.06)",
      },
      backdropBlur: {
        xs: "2px",
      },
      animation: {
        shimmer: "shimmer 1.8s ease-in-out infinite",
      },
      keyframes: {
        shimmer: {
          "0%": { backgroundPosition: "-200% 0" },
          "100%": { backgroundPosition: "200% 0" },
        },
      },
    },
  },
  plugins: [],
} satisfies Config;
