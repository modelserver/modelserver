import {
  createContext,
  useCallback,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import type { User, AuthResponse, DataResponse } from "@/api/types";
import {
  api,
  setTokens,
  clearTokens,
  getAccessToken,
  getStoredRefreshToken,
} from "@/api/client";

export interface AuthContextValue {
  user: User | null;
  loading: boolean;
  oauthLogin: (provider: string, code: string, state?: string) => Promise<string | undefined>;
  logout: () => void;
  refreshUser: () => Promise<void>;
}

export const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  const handleAuthResponse = useCallback((data: AuthResponse) => {
    setTokens(data.access_token, data.refresh_token);
    setUser(data.user);
  }, []);

  const refreshUser = useCallback(async () => {
    try {
      const res = await api.get<DataResponse<User>>("/api/v1/me");
      setUser(res.data);
    } catch {
      setUser(null);
    }
  }, []);

  // On mount, try to restore session from stored refresh token
  useEffect(() => {
    async function restore() {
      const rt = getStoredRefreshToken();
      if (!rt && !getAccessToken()) {
        setLoading(false);
        return;
      }
      try {
        if (!getAccessToken() && rt) {
          const res = await api.post<AuthResponse>("/api/v1/auth/refresh", {
            refresh_token: rt,
          });
          handleAuthResponse(res);
        } else {
          await refreshUser();
        }
      } catch {
        clearTokens();
      } finally {
        setLoading(false);
      }
    }
    restore();
  }, [handleAuthResponse, refreshUser]);

  const oauthLogin = useCallback(
    async (provider: string, code: string, state?: string): Promise<string | undefined> => {
      const res = await api.post<AuthResponse & { redirect_to?: string }>(
        `/api/v1/auth/oauth/${provider}`,
        { code, state },
      );
      // Hydra login flow: server returns redirect_to instead of tokens.
      if (res.redirect_to) {
        return res.redirect_to;
      }
      handleAuthResponse(res);
      return undefined;
    },
    [handleAuthResponse],
  );

  const logout = useCallback(() => {
    clearTokens();
    setUser(null);
  }, []);

  return (
    <AuthContext.Provider
      value={{
        user,
        loading,
        oauthLogin,
        logout,
        refreshUser,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}
