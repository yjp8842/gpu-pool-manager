import { Dashboard } from './pages/Dashboard';

// npm run dev:mock으로 띄우면 목업 API(MSW)를 쓰고, 화면에 "샘플 데이터"를 표시한다.
const sample = import.meta.env.VITE_MOCK === '1';

export default function App() {
  return <Dashboard sample={sample} />;
}
