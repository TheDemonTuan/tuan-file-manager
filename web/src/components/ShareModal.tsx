import React, { useState } from 'react';
import { Share2, Copy, Check } from 'lucide-react';
import { CreateShareResult } from '../types.ts';

interface Props {
  nodeId: string;
  nodeName: string;
  onClose: () => void;
}

export const ShareModal: React.FC<Props> = ({ nodeId, nodeName, onClose }) => {
  const [password, setPassword] = useState('');
  const [copied, setCopied] = useState(false);
  const [createdResult, setCreatedResult] = useState<CreateShareResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const handleCreate = async () => {
    setLoading(true);
    setError('');
    try {
      const res = await fetch('/api/v1/shares', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          target_node_id: nodeId,
          password: password || undefined,
        }),
      });
      if (!res.ok) {
        const err = await res.json();
        throw new Error(err.detail || 'Failed to create share link');
      }
      const data: CreateShareResult = await res.json();
      setCreatedResult(data);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  };

  const getShareUrl = () => {
    if (!createdResult) return '';
    return createdResult.share_url || `${window.location.origin}/s/${createdResult.token}`;
  };

  const handleCopy = () => {
    navigator.clipboard.writeText(getShareUrl());
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-content" onClick={(e) => e.stopPropagation()}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '1rem' }}>
          <Share2 size={20} color="var(--primary)" />
          <h3 style={{ fontSize: '1.125rem' }}>Share "{nodeName}"</h3>
        </div>

        {error && (
          <div style={{ padding: '0.5rem', background: 'rgba(239, 68, 68, 0.2)', border: '1px solid var(--danger)', borderRadius: '0.375rem', marginBottom: '1rem', color: '#fca5a5', fontSize: '0.875rem' }}>
            {error}
          </div>
        )}

        {!createdResult ? (
          <div>
            <div style={{ marginBottom: '1rem' }}>
              <label style={{ display: 'block', fontSize: '0.75rem', color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>
                Optional Password
              </label>
              <input
                type="password"
                className="input-field"
                style={{ width: '100%' }}
                placeholder="Leave blank for public access"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
              <button className="btn btn-secondary" onClick={onClose}>Cancel</button>
              <button className="btn btn-primary" onClick={handleCreate} disabled={loading}>
                {loading ? 'Generating...' : 'Create Share Link'}
              </button>
            </div>
          </div>
        ) : (
          <div>
            <p style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.75rem' }}>
              Anyone with this link can view and download:
            </p>
            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1.25rem' }}>
              <input
                type="text"
                readOnly
                className="input-field"
                style={{ width: '100%', fontFamily: 'monospace', fontSize: '0.8rem' }}
                value={getShareUrl()}
              />
              <button className="btn btn-primary" onClick={handleCopy}>
                {copied ? <Check size={16} /> : <Copy size={16} />}
              </button>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <button className="btn btn-secondary" onClick={onClose}>Done</button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};
