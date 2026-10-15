// The dashboard's icons are Lucide's, as in Agent Harness. Each keeps the `icon` class the stylesheet sizes, and is
// hidden from assistive technology unless given a label.
import type { LucideIcon } from "lucide-react";
import {
  ArrowUp,
  ChartColumn,
  ChevronDown,
  ChevronRight,
  Code,
  Copy,
  FileText,
  Folder,
  Info,
  LayoutGrid,
  Link,
  Moon,
  PanelLeft,
  RefreshCw,
  Search,
  Settings,
  Shield,
  Sun,
  X,
} from "lucide-react";

export function Icon({ of: Glyph, label }: { of: LucideIcon; label?: string }) {
  return (
    <Glyph
      className="icon"
      size={16}
      strokeWidth={2}
      aria-hidden={label ? undefined : true}
      aria-label={label}
      role={label ? "img" : undefined}
    />
  );
}

export const FolderIcon = () => <Icon of={Folder} />;
export const FileIcon = () => <Icon of={FileText} />;
export const CopyIcon = () => <Icon of={Copy} />;
export const LinkIcon = () => <Icon of={Link} />;
export const OverviewIcon = () => <Icon of={LayoutGrid} />;
export const ChartIcon = () => <Icon of={ChartColumn} />;
export const SearchIcon = () => <Icon of={Search} />;
export const CodeIcon = () => <Icon of={Code} />;
export const ShieldIcon = () => <Icon of={Shield} />;
export const SettingsIcon = () => <Icon of={Settings} />;
export const MenuIcon = () => <Icon of={PanelLeft} />;
export const CloseIcon = () => <Icon of={X} />;
export const RefreshIcon = () => <Icon of={RefreshCw} />;
export const InfoIcon = () => <Icon of={Info} />;
export const SunIcon = () => <Icon of={Sun} />;
export const MoonIcon = () => <Icon of={Moon} />;
export const ChevronIcon = ({ open = false }: { open?: boolean }) => <Icon of={open ? ChevronDown : ChevronRight} />;
export const UpIcon = () => <Icon of={ArrowUp} />;
