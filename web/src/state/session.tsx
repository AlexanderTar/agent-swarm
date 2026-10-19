import { createContext, useContext } from "react";

/** Who is using the board: `remote` is any origin other than this Mac (it signs in with a cookie). */
export interface SessionInfo {
  remote: boolean;
  email?: string;
  signOut(): void;
}

const Ctx = createContext<SessionInfo>({ remote: false, signOut: () => {} });

export const SessionProvider = Ctx.Provider;
export const useSession = () => useContext(Ctx);
