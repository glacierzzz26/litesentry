// antd 主题 —— 与 src/index.css 的 --* 令牌同源（浅色 / 深色两套）。
// 由 v2 App 依据 <html data-theme> 选择，切换时整树换肤。

import { theme } from 'antd';
import type { ThemeConfig } from 'antd';

/** 浅色主题（对齐 v1 既有 THEME）。 */
export const THEME_LIGHT: ThemeConfig = {
  token: {
    colorPrimary: '#2a78d6',
    colorInfo: '#2a78d6',
    colorLink: '#2a78d6',
    colorBgLayout: '#f4f6f9',
    colorBgContainer: '#ffffff',
    colorBgElevated: '#ffffff',
    colorBorder: '#e8ebf0',
    colorBorderSecondary: '#e8ebf0',
    colorSplit: '#e8ebf0',
    colorText: '#0f172a',
    colorTextSecondary: '#64748b',
    colorTextTertiary: '#94a3b8',
    colorTextQuaternary: '#cbd5e1',
    colorSuccess: '#16a34a',
    colorError: '#dc2626',
    colorWarning: '#d97706',
    fontSize: 14,
    borderRadius: 8,
  },
  components: {
    Menu: {
      itemBg: 'transparent',
      itemSelectedBg: '#eff5fd',
      itemSelectedColor: '#2a78d6',
      itemHoverBg: '#f4f6f9',
      itemBorderRadius: 8,
      itemMarginInline: 8,
      itemHeight: 40,
      activeBarBorderWidth: 0,
    },
    Table: {
      headerBg: '#f8fafc',
      headerColor: '#64748b',
      cellPaddingBlock: 13,
      cellPaddingInline: 16,
      rowHoverBg: '#f8fafc',
    },
    Layout: { headerBg: '#ffffff', headerHeight: 56, headerPadding: '0 20px', siderBg: '#ffffff' },
    Card: { borderRadiusLG: 10 },
  },
};

/** 深色主题（对齐 index.css [data-theme="dark"] 令牌）。 */
export const THEME_DARK: ThemeConfig = {
  algorithm: theme.darkAlgorithm,
  token: {
    colorPrimary: '#5aa0ec',
    colorInfo: '#5aa0ec',
    colorLink: '#5aa0ec',
    colorBgLayout: '#0f1115',
    colorBgContainer: '#161a20',
    colorBgElevated: '#1b2027',
    colorBorder: '#232831',
    colorBorderSecondary: '#2a2f38',
    colorSplit: '#232831',
    colorText: '#e6e9ef',
    colorTextSecondary: '#8b93a1',
    colorTextTertiary: '#5b6371',
    colorTextQuaternary: '#4a5260',
    colorSuccess: '#34d399',
    colorError: '#f87171',
    colorWarning: '#fbbf24',
    colorFillSecondary: '#232831',
    colorFillTertiary: '#232831',
    fontSize: 14,
    borderRadius: 8,
  },
  components: {
    Menu: {
      itemBg: 'transparent',
      itemSelectedBg: 'rgba(90,160,236,.14)',
      itemSelectedColor: '#5aa0ec',
      itemHoverBg: '#1b2027',
      itemBorderRadius: 8,
      itemMarginInline: 8,
      itemHeight: 40,
      activeBarBorderWidth: 0,
    },
    Table: {
      headerBg: '#1b2027',
      headerColor: '#8b93a1',
      cellPaddingBlock: 13,
      cellPaddingInline: 16,
      rowHoverBg: '#1b2027',
      borderColor: '#232831',
    },
    Layout: { headerBg: '#161a20', headerHeight: 56, headerPadding: '0 20px', siderBg: '#161a20' },
    Card: { borderRadiusLG: 10 },
    Modal: { contentBg: '#161a20', headerBg: '#161a20' },
  },
};

/** 从 <html data-theme> 读当前主题。 */
export function currentTheme(): 'light' | 'dark' {
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}

/** 切主题：同步 <html data-theme> + localStorage（与 index.html 防闪脚本共用 ls_theme 键）。 */
export function applyTheme(mode: 'light' | 'dark') {
  document.documentElement.setAttribute('data-theme', mode);
  try {
    localStorage.setItem('ls_theme', mode);
  } catch {
    /* 隐私模式静默 */
  }
}
