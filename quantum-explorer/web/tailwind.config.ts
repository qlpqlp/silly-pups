import type { Config } from "tailwindcss";

export default {
  darkMode: "class",
  content: ["./src/**/*.{js,ts,jsx,tsx,mdx}"],
  theme: {
    extend: {
      fontFamily: {
        comic: ["var(--font-comic)", "Comic Neue", "cursive", "system-ui"],
        sans: ["var(--font-comic)", "ui-sans-serif", "system-ui"],
      },
      colors: {
        doge: {
          gold: "#f2c94c",
          deep: "#c99400",
          ink: "#1c1410",
        },
      },
      boxShadow: {
        glass: "0 8px 32px rgba(0,0,0,0.12)",
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
