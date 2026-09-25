/* Identity tokens (name / accent / favicon). Scaffold writes starter values.
 * Visual system comes from repo-root DESIGN.md → index.css. Changing this file
 * is not enough to finish frontend.
 */
export const brand = {
  appName: "bps 2API",
  logoText: "BP",
  version: "v1.0",
  primaryColor: "#10a37f",
  githubUrl: "",
  siteDomain: "https://bps.openai.com",
  footer: "bps 2API · admin",
  faviconLetter: "B",
  locale: "zh-CN",
} as const;

export const siteHost = brand.siteDomain
  .replace(/^https?:\/\//, "")
  .replace(/\/.*$/, "");

export const appSlug = brand.appName.toLowerCase().replace(/\s+/g, "-");

/** Apply brand to document title, lang, favicon, and ink token. Call once at boot. */
export function applyBrand() {
  document.title = `${brand.appName} · 管理后台`;
  document.documentElement.lang = brand.locale;
  document.documentElement.style.setProperty("--brand", brand.primaryColor);
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (!link) {
    link = document.createElement("link");
    link.rel = "icon";
    document.head.appendChild(link);
  }
  link.href = "/favicon.svg";
}
