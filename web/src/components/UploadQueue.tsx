import React, { useState } from 'react';
import { Play, Pause, X, AlertCircle, CheckCircle2 } from 'lucide-react';
import { formatBytes } from '../utils.ts';

interface Props {
  queue: UploadItem[];
  onPause: (id: string) => void;
  onResume: (id: string) => void;
  onCancel: (id: string) => void;
}

export const UploadQueue: React.FC<Props> = ({ queue, onPause, onResume, onCancel }) => {
  const [minimized, setMinimized] = useState(false);

  if (queue.length === 0) return null;

  return (
    <div style={{
      position: 'fixed',
      bottom: '1rem',
      right: '1rem',
      width: '380px',
      maxHeight: minimized ? '48px' : '400px',
      background: 'var(--bg-surface)',
      border: '1px solid var(--border)',
      borderRadius: '0.5rem',
      boxShadow: '0 10px 25px -5px rgba(0, 0, 0, 0.5)',
      display: 'flex',
      flexDirection: 'column',
      zIndex: 30,
      overflow: 'hidden',
      transition: 'max-height 0.2s ease',
    }}>
      <div style={{
        padding: '0.75rem 1rem',
        background: 'var(--bg-card)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        cursor: 'pointer',
      }} onClick={() => setMinimized(!minimized)}>
        <span style={{ fontWeight: 600, fontSize: '0.875rem' }}>
          Uploads ({queue.filter(q => q.status === 'completed').length}/{queue.length})
        </span>
        <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
          {minimized ? 'Expand' : 'Minimize'}
        </span>
      </div>

      {!minimized && (
        <div style={{ padding: '0.75rem', overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
          {queue.map(item => {
            const pct = item.total > 0 ? Math.round((item.progress / item.total) * 100) : 0;
            return (
              <div key={item.id} style={{
                background: 'var(--bg-main)',
                padding: '0.5rem 0.75rem',
                borderRadius: '0.375rem',
                border: '1px solid var(--border)',
                fontSize: '0.75rem',
              }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.25rem' }}>
                  <span style={{ fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '180px' }}>
                    {item.file.name}
                  </span>
                  <div style={{ display: 'flex', gap: '0.25rem' }}>
                    {item.status === 'uploading' && (
                      <button className="btn btn-secondary" style={{ padding: '2px 4px' }} onClick={() => onPause(item.id)}>
                        <Pause size={12} />
                      </button>
                    )}
                    {item.status === 'paused' && (
                      <button className="btn btn-primary" style={{ padding: '2px 4px' }} onClick={() => onResume(item.id)}>
                        <Play size={12} />
                      </button>
                    )}
                    {item.status !== 'completed' && (
                      <button className="btn btn-danger" style={{ padding: '2px 4px' }} onClick={() => onCancel(item.id)}>
                        <X size={12} />
                      </button>
                    )}
                  </div>
                </div>

                {/* Progress bar */}
                <div style={{ width: '100%', height: '4px', background: 'var(--border)', borderRadius: '2px', overflow: 'hidden', margin: '4px 0' }}>
                  <div style={{
                    width: `${pct}%`,
                    height: '100%',
                    background: item.status === 'error' ? 'var(--danger)' : item.status === 'completed' ? 'var(--success)' : 'var(--primary)',
                  }} />
                </div>

                <div style={{ display: 'flex', justifyContent: 'space-between', color: 'var(--text-secondary)' }}>
                  <span>{formatBytes(item.progress)} / {formatBytes(item.total)} ({pct}%)</span>
                  <span>
                    {item.status === 'uploading' && 'Uploading...'}
                    {item.status === 'paused' && 'Paused'}
                    {item.status === 'completed' && <span style={{ color: 'var(--success)', display: 'flex', alignItems: 'center', gap: '2px' }}><CheckCircle2 size={12} /> Done</span>}
                    {item.status === 'error' && <span style={{ color: 'var(--danger)', display: 'flex', alignItems: 'center', gap: '2px' }}><AlertCircle size={12} /> Error</span>}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
};

export interface UploadItem {
  id: string;
  file: File;
  parentID: string;
  progress: number;
  total: number;
  status: 'pending' | 'uploading' | 'paused' | 'completed' | 'error';
  tusUpload?: any;
  error?: string;
}
