export interface User {
  identity: string;
  email: string;
  role: string;
  auth_type: string;
}

export interface FileObject {
  node_id: string;
  storage_key: string;
  size_bytes: number;
  mime_sniffed: string;
  sha256?: string;
  backup_selected_at?: string;
  created_at: string;
  updated_at: string;
}

export interface Node {
  id: string;
  parent_id: string | null;
  kind: 'file' | 'folder';
  name: string;
  name_key: string;
  created_at: string;
  updated_at: string;
  trashed_at?: string;
  restore_parent_id?: string;
  is_system: boolean;
  file_object?: FileObject;
}

export interface ListNodesResult {
  items: Node[];
  next_cursor?: string;
}

export interface StorageMetrics {
  stored_bytes: number;
  reserved_bytes: number;
  total_used_bytes: number;
  max_storage_bytes: number;
  node_count: number;
}

export interface Share {
  id: string;
  target_node_id: string;
  has_password: boolean;
  expires_at?: string;
  revoked_at?: string;
  created_at: string;
  updated_at: string;
  target_node?: Node;
}

export interface CreateShareResult {
  share: Share;
  token: string;
}

export interface PublicShareView {
  id: string;
  target_name: string;
  target_kind: string;
  size_bytes: number;
  need_password: boolean;
  unlocked: boolean;
  target_node?: Node;
}
