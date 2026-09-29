/**
 * GENERATED FILE — do not edit by hand. Edit src/config_web/design/tokens.mjs
 * and run: node scripts/gen_config_web_tokens.mjs
 *
 * Import these values in the companion app so a WebView-rendered settings
 * surface and a native screen cannot drift apart:
 *
 *   import {tokens} from './tokens.native';
 *
 * Notes for the React Native side:
 *   - `size.hairline` is 0.5 for the web. Use `StyleSheet.hairlineWidth` natively.
 *   - `motion.sheetBezier` feeds `Easing.bezier(...)`; `motion.sheet` is in ms.
 *   - `font.family` is a CSS stack. Native uses the platform system font,
 *     which is the same typeface the stack resolves to on iOS.
 */

/** @type {const} */
export const tokens = {
  color: {
    canvas: "#F6F8FA",
    card: "#FFFFFF",
    ink: "#111214",
    inkSecondary: "#8A8A8E",
    inkPlaceholder: "#9A9AA0",
    inkTertiary: "#C7C7CC",
    chevron: "#738294",
    separator: "#F3F3F3",
    accent: "#4B8EFF",
    success: "#34C759",
    danger: "#FF3B30",
    primaryFill: "#000000",
    primaryFillDisabled: "#D2D5DD",
    primaryInk: "#FFFFFF",
    fieldBorder: "#D6D6DA",
    inputBorder: "rgba(230, 230, 230, 0.45)",
    inputFocus: "#5478FF",
    inputInvalid: "#FF5454",
    switchTrackOff: "#D9D9D9",
    overlay: "rgba(0, 0, 0, 0.4)",
    sheetTitle: "#545B66",
    optionBorder: "#E5E7EB",
    optionSelected: "#F7F8FA",
    successTint: "#E9F9EE",
    actionSheetSurface: "rgba(249, 249, 249, 0.94)",
    actionSheetSeparator: "rgba(60, 60, 67, 0.29)",
    actionSheetTint: "#007AFF",
    pressed: "rgba(0, 0, 0, 0.05)",
    selected: "rgba(0, 0, 0, 0.06)",
    toast: "rgba(17, 18, 20, 0.92)",
    codeString: "#1E9E4A",
    codeLiteral: "#E0532D",
    codeSelection: "rgba(75, 142, 255, 0.25)"
  },
  space: {
    xs: 4,
    sm: 8,
    md: 12,
    lg: 16,
    xl: 20,
    xxl: 24
  },
  radius: {
    card: 12,
    field: 10,
    sheet: 20,
    tile: 12,
    actionSheet: 14,
    option: 16,
    optionIcon: 10,
    pill: 999
  },
  size: {
    row: 56,
    control: 50,
    tile: 46,
    icon: 18,
    chevron: 13,
    actionSheetButton: 57,
    actionSheetInset: 10,
    mark: 32,
    rowCompact: 48,
    fieldAction: 48,
    option: 66,
    optionIcon: 40,
    sheetHandle: 36,
    sheetGrabber: 24,
    sheetTop: 30,
    hairline: 0.5
  },
  font: {
    body: 17,
    bodySm: 15,
    caption: 13,
    title: 20,
    lineBody: 22,
    lineCaption: 18,
    lineCode: 20,
    sheetTitle: 14,
    actionSheetText: 13,
    actionSheetButton: 20,
    weightRegular: 400,
    weightMedium: 500,
    weightSemibold: 600,
    weightBold: 700,
    family: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Helvetica Neue', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif",
    familyMono: "ui-monospace, 'SF Mono', Menlo, Consolas, monospace"
  },
  motion: {
    sheet: 320,
    fast: 180,
    sheetBezier: [
      0.32,
      0.72,
      0,
      1
    ]
  },
  shadow: {
    sheet: {
      offsetX: 0,
      offsetY: -8,
      blur: 36,
      color: "rgba(0, 0, 0, 0.22)"
    },
    knob: {
      offsetX: 0,
      offsetY: 3,
      blur: 8,
      color: "rgba(0, 0, 0, 0.15)"
    },
    dropdown: {
      offsetX: 0,
      offsetY: 8,
      blur: 24,
      color: "rgba(0, 0, 0, 0.12)"
    },
    raised: {
      offsetX: 0,
      offsetY: 1,
      blur: 3,
      color: "rgba(0, 0, 0, 0.08)"
    }
  },
  layout: {
    pageMargin: 20,
    navWidth: 350,
    breakpointDesktop: 900,
    maxContent: 720
  }
};

export const {color, space, radius, size, font, motion, layout} = tokens;

export default tokens;
