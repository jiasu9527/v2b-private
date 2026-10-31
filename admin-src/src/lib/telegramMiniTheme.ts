import type { ThemeConfig } from 'antd';

export type TelegramThemeParams = Partial<Record<
  'bg_color' | 'secondary_bg_color' | 'section_bg_color' | 'header_bg_color' | 'bottom_bar_bg_color' |
  'text_color' | 'hint_color' | 'subtitle_text_color' | 'link_color' | 'button_color' |
  'button_text_color' | 'accent_text_color' | 'section_header_text_color' | 'section_separator_color' |
  'destructive_text_color', string
>>;

// Telegram supplies opaque RGB colors. Reject arbitrary CSS and retain a usable
// palette on older clients that provide only some of the theme fields.
function color(value: unknown, fallback: string): string {
  if (typeof value !== 'string' || !/^#(?:[\da-f]{3}|[\da-f]{6})$/i.test(value)) return fallback;
  const hex = value.slice(1).toLowerCase();
  return `#${hex.length === 3 ? hex.split('').map((part) => part + part).join('') : hex}`;
}

export function telegramMiniTheme(scheme?: string, params: TelegramThemeParams = {}) {
  const dark = scheme === 'dark';
  const bg = color(params.bg_color, dark ? '#212121' : '#ffffff');
  const text = color(params.text_color, dark ? '#f5f5f5' : '#111111');
  const hint = color(params.hint_color, dark ? '#aaaaaa' : '#707579');
  const button = color(params.button_color, dark ? '#8774e1' : '#3390ec');
  const link = color(params.link_color, button);
  const colors: Required<TelegramThemeParams> = {
    bg_color: bg,
    secondary_bg_color: color(params.secondary_bg_color, dark ? '#181818' : '#f1f3f5'),
    section_bg_color: color(params.section_bg_color, bg),
    header_bg_color: color(params.header_bg_color, bg),
    bottom_bar_bg_color: color(params.bottom_bar_bg_color, bg),
    text_color: text,
    hint_color: hint,
    subtitle_text_color: color(params.subtitle_text_color, hint),
    link_color: link,
    button_color: button,
    button_text_color: color(params.button_text_color, '#ffffff'),
    accent_text_color: color(params.accent_text_color, link),
    section_header_text_color: color(params.section_header_text_color, link),
    section_separator_color: color(params.section_separator_color, dark ? '#353535' : '#e7e9eb'),
    destructive_text_color: color(params.destructive_text_color, dark ? '#ff6666' : '#d94b4b'),
  };
  const variables = Object.fromEntries(Object.entries(colors).map(([name, value]) => [`--tg-theme-${name.replace(/_/g, '-')}`, value]));
  const tokens: ThemeConfig['token'] = {
    colorPrimary: button,
    colorLink: link,
    colorText: text,
    colorTextSecondary: hint,
    colorBgBase: bg,
    colorBgLayout: colors.secondary_bg_color,
    colorBgContainer: colors.section_bg_color,
    colorBgElevated: colors.section_bg_color,
    colorBorder: colors.section_separator_color,
    colorBorderSecondary: colors.section_separator_color,
    colorError: colors.destructive_text_color,
    colorTextLightSolid: colors.button_text_color,
    borderRadius: 12,
    controlHeight: 40,
    fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif',
  };
  return { dark, colors, variables, tokens };
}

export function miniSafeInset(value: unknown): number {
  const number = Number(value);
  return Number.isFinite(number) ? Math.min(200, Math.max(0, number)) : 0;
}

export function miniViewportHeight(sdkHeight: unknown, visualHeight: number): number {
  const height = Number(sdkHeight);
  return Number.isFinite(height) && height > 0 ? Math.min(height, visualHeight) : visualHeight;
}
