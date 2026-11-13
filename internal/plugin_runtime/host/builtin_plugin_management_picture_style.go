package host

const pluginCommandCatalogStyle = `
* { box-sizing: border-box; }
body { margin: 0; background: white; color: hsl(222.2 84% 4.9%);
  font: 16px/1.6 -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Microsoft YaHei', sans-serif; }
main { padding: 36px; }
.catalog.two-columns { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; }
.column { min-width: 0; }
header { border-bottom: 1px solid hsl(188.5 20% 91.4%); padding-bottom: 20px; margin-bottom: 24px; }
.brand { color: hsl(28.9 94.8% 45.1%); font-size: 13px; font-weight: 600; letter-spacing: 2px; }
h1 { font-size: 28px; margin: 6px 0; letter-spacing: -.02em; }
.muted, dt, footer { color: hsl(188.5 20% 46.9%); }
section { border: 1px solid hsl(188.5 20% 91.4%); border-radius: 12px;
  background: hsl(188.5 14% 98.6%); margin-top: 18px; overflow: hidden; }
h2 { margin: 0; padding: 16px 20px; font-size: 18px; border-bottom: 1px solid hsl(188.5 20% 91.4%); }
article { padding: 16px 20px; background: white; }
article + article { border-top: 1px solid hsl(188.5 20% 91.4%); }
h3 { margin: 0; font-size: 17px; }
.badge { display: inline-block; margin-left: 10px; padding: 1px 8px; border-radius: 6px;
  background: hsl(112.7 40.2% 94%); color: hsl(112.7 40.2% 30%); font-size: 12px; font-weight: 500; }
.disabled { background: hsl(188.5 12% 96%); color: hsl(188.5 20% 46.9%); }
dl { margin: 8px 0 0; display: grid; grid-template-columns: 76px minmax(0, 1fr); gap: 5px 12px; }
dd { margin: 0; white-space: pre-wrap; }
h2, h3, dd { overflow-wrap: anywhere; }
code { font: 14px/1.6 'JetBrains Mono', Consolas, 'Microsoft YaHei', monospace; }
footer { margin-top: 20px; font-size: 13px; }
`

const pluginCommandCatalogFutureRetroStyle = `
body { background: #f3e3cc; color: #0a4550; font-weight: 700;
  background-image: repeating-linear-gradient(0deg, #0a455008 0 1px, transparent 1px 4px); }
header { border-bottom: 3px solid #0a4550; }
.brand { color: #c24d24; }
.muted, dt, footer { color: #0d4650ad; }
section { border: 2px solid #0d4650d1; border-radius: 4px; background: #f3e3cc;
  box-shadow: 4px 4px 0 #49301624; }
h2 { border-bottom: 2px solid #0d4650d1; background: #dec5a5; }
article { background: transparent; }
article + article { border-top: 1px solid #0d465066; }
.badge { border-radius: 2px; border: 1px solid #0a4550; }
.disabled { background: #dec5a5; color: #0a4550; }
`

const pluginCommandCatalogMillenniumStyle = `
body { background: #ddd4bf; color: #4a443a;
  background-image: radial-gradient(#5a544a17 1px, transparent 1px); background-size: 4px 4px; }
header { padding: 20px 24px; background: #ebe4d2; border: 2px solid #5a544a;
  box-shadow: inset 2px 2px 0 #f6f1e3, inset -2px -2px 0 #bdb39c, 0 5px 0 #9c927b; }
.brand { color: #2a7d50; }
.muted, dt, footer { color: #5f5849; }
section { background: #ebe4d2; border: 2px solid #5a544a; border-radius: 0; padding: 12px;
  box-shadow: inset 2px 2px 0 #f6f1e3, inset -2px -2px 0 #bdb39c, 0 5px 0 #9c927b; }
h2 { border: 0; padding: 6px 8px 16px; letter-spacing: .04em; }
article { background: #fbf9f0; color: #3a3833; border: 1px solid #bdb39c;
  box-shadow: inset 3px 3px 0 #00000029; }
article + article { border-top: 1px solid #d3cec0; }
.badge { border-radius: 0; color: #2f3a2b; background: #8fd6a0; border: 1px solid #5a544a;
  box-shadow: inset 0 -2px 0 #5fa873, inset 1px 1px 0 #fffaf0; }
.disabled { background: #f3eee1; color: #5f5849; box-shadow: inset 0 -2px 0 #c2b8a1; }
`

var pluginCommandCatalogStyleNames = map[int]string{
	0: "modern",
	1: "future-retro",
	2: "millennium",
}

func pluginCommandCatalogStyleVariant(style int) string {
	switch style {
	case 1:
		return pluginCommandCatalogFutureRetroStyle
	case 2:
		return pluginCommandCatalogMillenniumStyle
	default:
		return ""
	}
}
