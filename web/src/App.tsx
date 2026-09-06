import React, { useState, useEffect, useCallback } from 'react';
import {
  Folder, File, Trash2, HardDrive, Search, Plus,
  Download, RefreshCw, Share2, Shield, Lock
} from 'lucide-react';
import { Node, StorageMetrics, User } from './types.ts';
import { formatBytes, formatDate } from './utils.ts';
import { UploadQueue, UploadItem } from './components/UploadQueue.tsx';
import { ShareModal } from './components/ShareModal.tsx';
import * as tus from 'tus-js-client';

export const App: React.FC = () => {
  // Navigation & state
  const [user, setUser] = useState<User | null>(null);
  const [currentFolderId, setCurrentFolderId] = useState<string>('ROOT');
  const [folderHistory, setFolderHistory] = useState<{ id: string; name: string }[]>([
    { id: 'ROOT', name: 'Root' },
  ]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [trashNodes, setTrashNodes] = useState<Node[]>([]);
  const [storage, setStorage] = useState<StorageMetrics | null>(null);
  const [view, setView] = useState<'files' | 'trash'>('files');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(false);
  const [isDragging, setIsDragging] = useState(false);

  // Upload queue
  const [queue, setQueue] = useState<UploadItem[]>([]);

  // Modals
  const [showNewFolder, setShowNewFolder] = useState(false);
  const [newFolderName, setNewFolderName] = useState('');
  const [shareTarget, setShareTarget] = useState<Node | null>(null);

  // Load items
  const loadNodes = useCallback(async () => {
    setLoading(true);
    try {
      const url = search
        ? `/api/v1/nodes?search=${encodeURIComponent(search)}`
        : `/api/v1/nodes?parent_id=${currentFolderId}`;
      const res = await fetch(url);
      if (res.ok) {
        const data = await res.json();
        setNodes(data.items || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [currentFolderId, search]);

  const loadTrash = async () => {
    try {
      const res = await fetch('/api/v1/trash');
      if (res.ok) {
        const data = await res.json();
        setTrashNodes(data || []);
      }
    } catch (e) {
      console.error(e);
    }
  };

  const loadStorage = async () => {
    try {
      const res = await fetch('/api/v1/system/storage');
      if (res.ok) {
        const data = await res.json();
        setStorage(data);
      }
    } catch (e) {
      console.error(e);
    }
  };

  useEffect(() => {
    fetch('/api/v1/me')
      .then(res => res.ok ? res.json() : null)
      .then(data => {
        if (data) setUser(data);
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (view === 'files') {
      loadNodes();
    } else {
      loadTrash();
    }
    loadStorage();
  }, [view, loadNodes]);

  // Folder navigation
  const openFolder = (folder: Node) => {
    setSearch('');
    setCurrentFolderId(folder.id);
    setFolderHistory([...folderHistory, { id: folder.id, name: folder.name }]);
  };

  const navigateToHistory = (index: number) => {
    setSearch('');
    const target = folderHistory[index];
    setFolderHistory(folderHistory.slice(0, index + 1));
    setCurrentFolderId(target.id);
  };

  // Create folder
  const handleCreateFolder = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newFolderName.trim()) return;
    try {
      const res = await fetch('/api/v1/folders', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ parent_id: currentFolderId, name: newFolderName.trim() }),
      });
      if (res.ok) {
        setNewFolderName('');
        setShowNewFolder(false);
        loadNodes();
      } else {
        const err = await res.json();
        alert(err.detail || 'Failed to create folder');
      }
    } catch (e: any) {
      alert(e.message);
    }
  };

  // Trash & purge
  const handleTrash = async (node: Node) => {
    if (!confirm(`Move "${node.name}" to trash?`)) return;
    try {
      await fetch(`/api/v1/nodes/${node.id}/trash`, { method: 'POST' });
      loadNodes();
      loadStorage();
    } catch (e) {
      console.error(e);
    }
  };

  const handleRestore = async (node: Node) => {
    try {
      await fetch(`/api/v1/trash/${node.id}/restore`, { method: 'POST' });
      loadTrash();
      loadStorage();
    } catch (e) {
      console.error(e);
    }
  };

  const handlePurge = async (node: Node) => {
    if (!confirm(`Permanently delete "${node.name}"? This cannot be undone.`)) return;
    try {
      await fetch(`/api/v1/trash/${node.id}`, { method: 'DELETE' });
      loadTrash();
      loadStorage();
    } catch (e) {
      console.error(e);
    }
  };

  // Tus upload handler
  const startUpload = (file: File) => {
    const uploadId = Math.random().toString(36).substring(2, 9);
    const item: UploadItem = {
      id: uploadId,
      file,
      parentID: currentFolderId,
      progress: 0,
      total: file.size,
      status: 'uploading',
    };

    const upload = new tus.Upload(file, {
      endpoint: '/api/v1/uploads/tus',
      retryDelays: [0, 3000, 5000, 10000, 20000],
      chunkSize: 16 * 1024 * 1024, // 16 MiB chunk
      metadata: {
        filename: file.name,
        parent_id: currentFolderId,
      },
      onError: (err) => {
        setQueue(prev => prev.map(q => q.id === uploadId ? { ...q, status: 'error', error: err.message } : q));
      },
      onProgress: (bytesSent, bytesTotal) => {
        setQueue(prev => prev.map(q => q.id === uploadId ? { ...q, progress: bytesSent, total: bytesTotal } : q));
      },
      onSuccess: () => {
        setQueue(prev => prev.map(q => q.id === uploadId ? { ...q, status: 'completed' } : q));
        loadNodes();
        loadStorage();
      },
    });

    item.tusUpload = upload;
    setQueue(prev => [item, ...prev]);
    upload.start();
  };

  const handleFiles = (fileList: FileList | null) => {
    if (!fileList) return;
    Array.from(fileList).forEach(file => startUpload(file));
  };

  // Drag and drop
  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(true);
  };
  const handleDragLeave = () => setIsDragging(false);
  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    handleFiles(e.dataTransfer.files);
  };

  // Check if viewing public share (URL /s/:token)
  const isPublicShare = window.location.pathname.startsWith('/s/');
  if (isPublicShare) {
    return <PublicShareViewComponent />;
  }

  return (
    <div
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
      style={{ minHeight: '100vh', display: 'flex', flexDirection: 'column' }}
    >
      {isDragging && (
        <div className="drag-overlay">
          <h2 style={{ color: 'var(--text-primary)', pointerEvents: 'none' }}>Drop files anywhere to upload</h2>
        </div>
      )}

      {/* Top Navbar */}
      <header style={{
        background: 'var(--bg-surface)',
        borderBottom: '1px solid var(--border)',
        padding: '0.75rem 1.5rem',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        flexWrap: 'wrap',
        gap: '1rem',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <Folder size={24} color="var(--primary)" />
          <h1 style={{ fontSize: '1.25rem', fontWeight: 600 }}>File Manager</h1>
          <span style={{ fontSize: '0.75rem', background: 'rgba(59, 130, 246, 0.2)', color: 'var(--primary)', padding: '2px 8px', borderRadius: '4px', fontWeight: 500 }}>
            v2.1-prod
          </span>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '1.25rem', flexWrap: 'wrap' }}>
          {/* Security Posture Status */}
          <div style={{
            display: 'flex',
            alignItems: 'center',
            gap: '0.375rem',
            padding: '0.25rem 0.75rem',
            borderRadius: '9999px',
            backgroundColor: 'rgba(6, 78, 59, 0.4)',
            border: '1px solid rgba(6, 95, 70, 0.6)',
            color: '#34d399',
            fontSize: '0.75rem',
            fontWeight: 500,
          }}>
            <Shield size={14} color="#34d399" />
            <span>Zero Public Ports</span>
            <span style={{ opacity: 0.5 }}>•</span>
            <span>CF Tunnel</span>
          </div>

          {/* Storage Meter */}
          {storage && (
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.375rem' }}>
                <HardDrive size={15} />
                <span>
                  {formatBytes(storage.total_used_bytes)} / {formatBytes(storage.disk_total_bytes || storage.max_storage_bytes)}
                </span>
                {storage.disk_free_bytes ? (
                  <span style={{ fontSize: '0.75rem', color: '#10b981', marginLeft: '2px' }}>
                    ({formatBytes(storage.disk_free_bytes)} free)
                  </span>
                ) : null}
              </div>
              <div style={{ width: '80px', height: '6px', background: 'var(--border)', borderRadius: '3px', overflow: 'hidden' }}>
                <div style={{
                  width: `${Math.min(100, Math.max(2, Math.round((storage.total_used_bytes / (storage.disk_total_bytes || storage.max_storage_bytes || 1)) * 100)))}%`,
                  height: '100%',
                  background: 'var(--primary)',
                }} />
              </div>
            </div>
          )}

          {/* Cloudflare Access User Identity & Avatar */}
          {user ? (
            <div style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.75rem',
              paddingLeft: '1rem',
              borderLeft: '1px solid var(--border)',
            }}>
              <div style={{ textAlign: 'right', display: 'flex', flexDirection: 'column' }}>
                <span style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--text-primary)' }}>
                  {user.email}
                </span>
                <span style={{ fontSize: '0.675rem', color: '#38bdf8', display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: '4px' }}>
                  <span style={{ display: 'inline-block', width: '6px', height: '6px', borderRadius: '50%', backgroundColor: '#10b981' }} />
                  Cloudflare Authenticated
                </span>
              </div>

              <span style={{
                fontSize: '0.65rem',
                fontWeight: 700,
                letterSpacing: '0.05em',
                textTransform: 'uppercase',
                padding: '2px 6px',
                borderRadius: '4px',
                backgroundColor: 'rgba(16, 185, 129, 0.15)',
                color: '#10b981',
                border: '1px solid rgba(16, 185, 129, 0.3)',
              }}>
                {user.role}
              </span>

              {/* User Avatar Circle */}
              <div style={{
                width: '34px',
                height: '34px',
                borderRadius: '50%',
                background: 'linear-gradient(135deg, #3b82f6 0%, #8b5cf6 100%)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                color: '#ffffff',
                fontWeight: 700,
                fontSize: '0.875rem',
                boxShadow: '0 2px 8px rgba(0, 0, 0, 0.3)',
                border: '1px solid rgba(255, 255, 255, 0.2)',
                userSelect: 'none',
              }}>
                {user.email ? user.email.charAt(0).toUpperCase() : 'U'}
              </div>
            </div>
          ) : (
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>Authenticating...</div>
          )}
        </div>
      </header>

      {/* Main Layout */}
      <div style={{ display: 'flex', flex: 1 }}>
        {/* Sidebar */}
        <aside style={{
          width: '240px',
          background: 'var(--bg-surface)',
          borderRight: '1px solid var(--border)',
          padding: '1.5rem 1rem',
          display: 'flex',
          flexDirection: 'column',
          gap: '0.5rem',
        }}>
          <button
            className={`btn ${view === 'files' ? 'btn-primary' : 'btn-secondary'}`}
            style={{ justifyContent: 'flex-start', width: '100%' }}
            onClick={() => setView('files')}
          >
            <Folder size={18} /> Files
          </button>
          <button
            className={`btn ${view === 'trash' ? 'btn-primary' : 'btn-secondary'}`}
            style={{ justifyContent: 'flex-start', width: '100%' }}
            onClick={() => setView('trash')}
          >
            <Trash2 size={18} /> Trash
          </button>
        </aside>

        {/* Content Area */}
        <main style={{ flex: 1, padding: '1.5rem 2rem', display: 'flex', flexDirection: 'column', gap: '1.25rem' }}>
          {view === 'files' ? (
            <>
              {/* Action Toolbar */}
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '1rem' }}>
                {/* Breadcrumbs */}
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.9rem' }}>
                  {folderHistory.map((f, idx) => (
                    <React.Fragment key={f.id}>
                      {idx > 0 && <span style={{ color: 'var(--text-secondary)' }}>/</span>}
                      <button
                        style={{
                          background: 'none',
                          border: 'none',
                          color: idx === folderHistory.length - 1 ? 'var(--text-primary)' : 'var(--primary)',
                          cursor: 'pointer',
                          fontWeight: idx === folderHistory.length - 1 ? 600 : 400,
                        }}
                        onClick={() => navigateToHistory(idx)}
                      >
                        {f.name}
                      </button>
                    </React.Fragment>
                  ))}
                </div>

                {/* Right actions: search & buttons */}
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
                  <div style={{ position: 'relative' }}>
                    <Search size={16} style={{ position: 'absolute', left: '0.75rem', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }} />
                    <input
                      type="text"
                      className="input-field"
                      placeholder="Search files..."
                      style={{ paddingLeft: '2.25rem', width: '220px' }}
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                    />
                  </div>

                  <button className="btn btn-secondary" onClick={() => setShowNewFolder(true)}>
                    <Plus size={16} /> New Folder
                  </button>

                  <label className="btn btn-primary" style={{ margin: 0, cursor: 'pointer' }}>
                    <Download size={16} style={{ transform: 'rotate(180deg)' }} /> Upload Files
                    <input type="file" multiple style={{ display: 'none' }} onChange={(e) => handleFiles(e.target.files)} />
                  </label>

                  <button className="btn btn-secondary" onClick={loadNodes} title="Refresh">
                    <RefreshCw size={16} />
                  </button>
                </div>
              </div>

              {/* Files Table */}
              <div className="card" style={{ flex: 1, padding: 0, overflow: 'hidden' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left', fontSize: '0.875rem' }}>
                  <thead>
                    <tr style={{ background: 'var(--bg-card)', borderBottom: '1px solid var(--border)', color: 'var(--text-secondary)' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Name</th>
                      <th style={{ padding: '0.75rem 1rem', width: '120px' }}>Size</th>
                      <th style={{ padding: '0.75rem 1rem', width: '180px' }}>Modified</th>
                      <th style={{ padding: '0.75rem 1rem', width: '120px', textAlign: 'right' }}>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {loading ? (
                      <tr>
                        <td colSpan={4} style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-secondary)' }}>
                          Loading...
                        </td>
                      </tr>
                    ) : nodes.length === 0 ? (
                      <tr>
                        <td colSpan={4} style={{ padding: '3rem', textAlign: 'center', color: 'var(--text-secondary)' }}>
                          Folder is empty. Drag and drop files here to upload.
                        </td>
                      </tr>
                    ) : (
                      nodes.map((n) => (
                        <tr
                          key={n.id}
                          style={{ borderBottom: '1px solid rgba(71, 85, 105, 0.4)', transition: 'background-color 0.15s' }}
                          onMouseEnter={(e) => e.currentTarget.style.backgroundColor = 'var(--bg-card)'}
                          onMouseLeave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
                        >
                          <td style={{ padding: '0.75rem 1rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                            {n.kind === 'folder' ? (
                              <span
                                style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer', color: 'var(--primary)', fontWeight: 500 }}
                                onClick={() => openFolder(n)}
                              >
                                <Folder size={18} fill="currentColor" opacity={0.2} /> {n.name}
                              </span>
                            ) : (
                              <span style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                                <File size={18} color="var(--text-secondary)" /> {n.name}
                              </span>
                            )}
                          </td>
                          <td style={{ padding: '0.75rem 1rem', color: 'var(--text-secondary)' }}>
                            {n.file_object ? formatBytes(n.file_object.size_bytes) : '-'}
                          </td>
                          <td style={{ padding: '0.75rem 1rem', color: 'var(--text-secondary)' }}>
                            {formatDate(n.updated_at)}
                          </td>
                          <td style={{ padding: '0.75rem 1rem', textAlign: 'right' }}>
                            <div style={{ display: 'inline-flex', gap: '0.375rem' }}>
                              {n.kind === 'file' && (
                                <a
                                  href={`/api/v1/files/${n.id}/download`}
                                  className="btn btn-secondary"
                                  style={{ padding: '4px 6px' }}
                                  title="Download"
                                >
                                  <Download size={14} />
                                </a>
                              )}
                              <button
                                className="btn btn-secondary"
                                style={{ padding: '4px 6px' }}
                                onClick={() => setShareTarget(n)}
                                title="Share"
                              >
                                <Share2 size={14} />
                              </button>
                              <button
                                className="btn btn-secondary"
                                style={{ padding: '4px 6px' }}
                                onClick={() => handleTrash(n)}
                                title="Move to Trash"
                              >
                                <Trash2 size={14} />
                              </button>
                            </div>
                          </td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <>
              {/* Trash View */}
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <h2 style={{ fontSize: '1.25rem', fontWeight: 600 }}>Trash</h2>
                <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                  Items in trash are permanently purged after 30 days.
                </span>
              </div>

              <div className="card" style={{ flex: 1, padding: 0, overflow: 'hidden' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left', fontSize: '0.875rem' }}>
                  <thead>
                    <tr style={{ background: 'var(--bg-card)', borderBottom: '1px solid var(--border)', color: 'var(--text-secondary)' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Name</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Trashed Date</th>
                      <th style={{ padding: '0.75rem 1rem', textAlign: 'right' }}>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {trashNodes.length === 0 ? (
                      <tr>
                        <td colSpan={3} style={{ padding: '3rem', textAlign: 'center', color: 'var(--text-secondary)' }}>
                          Trash is empty.
                        </td>
                      </tr>
                    ) : (
                      trashNodes.map((n) => (
                        <tr key={n.id} style={{ borderBottom: '1px solid rgba(71, 85, 105, 0.4)' }}>
                          <td style={{ padding: '0.75rem 1rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                            {n.kind === 'folder' ? <Folder size={18} /> : <File size={18} />} {n.name}
                          </td>
                          <td style={{ padding: '0.75rem 1rem', color: 'var(--text-secondary)' }}>
                            {formatDate(n.trashed_at || '')}
                          </td>
                          <td style={{ padding: '0.75rem 1rem', textAlign: 'right' }}>
                            <div style={{ display: 'inline-flex', gap: '0.5rem' }}>
                              <button className="btn btn-secondary" onClick={() => handleRestore(n)}>
                                Restore
                              </button>
                              <button className="btn btn-danger" onClick={() => handlePurge(n)}>
                                Purge
                              </button>
                            </div>
                          </td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </main>
      </div>

      {/* New Folder Modal */}
      {showNewFolder && (
        <div className="modal-backdrop" onClick={() => setShowNewFolder(false)}>
          <div className="modal-content" onClick={(e) => e.stopPropagation()}>
            <h3 style={{ marginBottom: '1rem', fontSize: '1.125rem' }}>Create New Folder</h3>
            <form onSubmit={handleCreateFolder}>
              <input
                type="text"
                autoFocus
                className="input-field"
                style={{ width: '100%', marginBottom: '1rem' }}
                placeholder="Folder Name"
                value={newFolderName}
                onChange={(e) => setNewFolderName(e.target.value)}
              />
              <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
                <button type="button" className="btn btn-secondary" onClick={() => setShowNewFolder(false)}>
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">Create</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Share Modal */}
      {shareTarget && (
        <ShareModal
          nodeId={shareTarget.id}
          nodeName={shareTarget.name}
          onClose={() => setShareTarget(null)}
        />
      )}

      {/* Upload Queue Overlay */}
      <UploadQueue
        queue={queue}
        onPause={(id) => {
          const it = queue.find(q => q.id === id);
          if (it?.tusUpload) {
            it.tusUpload.abort();
            setQueue(prev => prev.map(q => q.id === id ? { ...q, status: 'paused' } : q));
          }
        }}
        onResume={(id) => {
          const it = queue.find(q => q.id === id);
          if (it?.tusUpload) {
            it.tusUpload.start();
            setQueue(prev => prev.map(q => q.id === id ? { ...q, status: 'uploading' } : q));
          }
        }}
        onCancel={(id) => {
          const it = queue.find(q => q.id === id);
          if (it?.tusUpload) it.tusUpload.abort();
          setQueue(prev => prev.filter(q => q.id !== id));
        }}
      />
    </div>
  );
};

// Public Share Download Component
const PublicShareViewComponent: React.FC = () => {
  const token = window.location.pathname.replace('/s/', '').split('/')[0];
  const [shareData, setShareData] = useState<any>(null);
  const [folderNodes, setFolderNodes] = useState<any[]>([]);
  const [password, setPassword] = useState('');
  const [unlocked, setUnlocked] = useState(false);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  const loadShare = async () => {
    try {
      const res = await fetch(`/s/${token}/meta`, {
        headers: { 'Accept': 'application/json' },
      });
      if (!res.ok) throw new Error('Share link expired or invalid');
      const data = await res.json();
      setShareData(data);
      setUnlocked(data.unlocked);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadShare();
  }, [token]);

  useEffect(() => {
    if (shareData?.target_kind === 'folder' && (unlocked || !shareData.need_password)) {
      fetch(`/s/${token}/nodes`, { headers: { 'Accept': 'application/json' } })
        .then(res => res.ok ? res.json() : null)
        .then(data => {
          if (data?.items) setFolderNodes(data.items);
        })
        .catch(() => {});
    }
  }, [shareData, unlocked, token]);

  const handleUnlock = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    try {
      const res = await fetch(`/s/${token}/unlock`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      });
      if (res.ok) {
        setUnlocked(true);
        loadShare();
      } else {
        const err = await res.json();
        setError(err.detail || 'Incorrect password');
      }
    } catch (e: any) {
      setError(e.message);
    }
  };

  if (loading) {
    return <div style={{ padding: '4rem', textAlign: 'center' }}>Loading share...</div>;
  }

  if (error && !shareData) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <div className="card" style={{ maxWidth: '400px', textAlign: 'center', padding: '2rem' }}>
          <h2 style={{ color: 'var(--danger)', marginBottom: '0.5rem' }}>Link Unavailable</h2>
          <p style={{ color: 'var(--text-secondary)' }}>{error}</p>
        </div>
      </div>
    );
  }

  const fileNodeId = shareData?.target_node?.id || shareData?.target_node_id;

  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '1rem' }}>
      <div className="card" style={{ width: '100%', maxWidth: '520px', padding: '2rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem' }}>
          <Shield size={28} color="var(--primary)" />
          <div>
            <h2 style={{ fontSize: '1.25rem', fontWeight: 600 }}>Shared Content</h2>
            <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>Secured via Cloudflare Zero Trust</p>
          </div>
        </div>

        {shareData?.need_password && !unlocked ? (
          <form onSubmit={handleUnlock}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '1rem', color: 'var(--text-secondary)', fontSize: '0.875rem' }}>
              <Lock size={16} /> This share link is password-protected.
            </div>
            {error && (
              <div style={{ padding: '0.5rem', background: 'rgba(239, 68, 68, 0.2)', border: '1px solid var(--danger)', borderRadius: '0.375rem', marginBottom: '1rem', color: '#fca5a5', fontSize: '0.875rem' }}>
                {error}
              </div>
            )}
            <input
              type="password"
              className="input-field"
              style={{ width: '100%', marginBottom: '1rem' }}
              placeholder="Enter password to unlock"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
            />
            <button type="submit" className="btn btn-primary" style={{ width: '100%', justifyContent: 'center' }}>
              Unlock Content
            </button>
          </form>
        ) : (
          <div>
            <div style={{
              background: 'var(--bg-card)',
              padding: '1.25rem',
              borderRadius: '0.5rem',
              marginBottom: '1.5rem',
              display: 'flex',
              alignItems: 'center',
              gap: '1rem',
            }}>
              {shareData.target_kind === 'folder' ? <Folder size={36} color="var(--primary)" /> : <File size={36} color="var(--primary)" />}
              <div style={{ flex: 1, minWidth: 0 }}>
                <h3 style={{ fontSize: '1.1rem', fontWeight: 600, wordBreak: 'break-all' }}>{shareData.target_name}</h3>
                <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                  {shareData.size_bytes > 0 ? formatBytes(shareData.size_bytes) : 'Folder'}
                </p>
              </div>
            </div>

            {shareData.target_kind === 'file' ? (
              <a
                href={`/s/${token}/files/${fileNodeId}/download`}
                className="btn btn-primary"
                style={{ width: '100%', justifyContent: 'center', padding: '0.75rem', fontSize: '1rem' }}
              >
                <Download size={18} /> Download File
              </a>
            ) : (
              <div>
                <h4 style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.75rem' }}>Files inside folder:</h4>
                {folderNodes.length === 0 ? (
                  <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem' }}>Folder is empty</p>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', maxHeight: '300px', overflowY: 'auto' }}>
                    {folderNodes.map(fn => (
                      <div key={fn.id} style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        background: 'var(--bg-main)',
                        padding: '0.5rem 0.75rem',
                        borderRadius: '0.375rem',
                        border: '1px solid var(--border)',
                      }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', overflow: 'hidden' }}>
                          <File size={16} color="var(--text-secondary)" />
                          <span style={{ fontSize: '0.875rem', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: '240px' }}>
                            {fn.name}
                          </span>
                        </div>
                        {fn.kind === 'file' && (
                          <a
                            href={`/s/${token}/files/${fn.id}/download`}
                            className="btn btn-secondary"
                            style={{ padding: '2px 6px' }}
                            title="Download"
                          >
                            <Download size={14} />
                          </a>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
};
